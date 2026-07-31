package main

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	tiledOlexFormat = "tiled-v1"
	tileLatDegrees  = 1.0
	tileLonDegrees  = 1.0
	maxOpenSpools   = 64
)

type OlexImportProgress struct {
	Phase      string  `json:"phase"`
	Progress   float64 `json:"progress"`
	BytesRead  int64   `json:"bytesRead"`
	TotalBytes int64   `json:"totalBytes"`
	ValidRows  int64   `json:"validRows"`
	TilesDone  int     `json:"tilesDone"`
	TilesTotal int     `json:"tilesTotal"`
	Detail     string  `json:"detail,omitempty"`
}

type OlexTileMeta struct {
	File                           string  `json:"file"`
	Records                        int     `json:"records"`
	SizeBytes                      int64   `json:"sizeBytes"`
	MinLat, MaxLat, MinLon, MaxLon float64 `json:"-"`
}

func (m OlexTileMeta) MarshalJSON() ([]byte, error) {
	type alias struct {
		File      string  `json:"file"`
		Records   int     `json:"records"`
		SizeBytes int64   `json:"sizeBytes"`
		MinLat    float64 `json:"minLat"`
		MaxLat    float64 `json:"maxLat"`
		MinLon    float64 `json:"minLon"`
		MaxLon    float64 `json:"maxLon"`
	}
	return json.Marshal(alias{m.File, m.Records, m.SizeBytes, m.MinLat, m.MaxLat, m.MinLon, m.MaxLon})
}

func (m *OlexTileMeta) UnmarshalJSON(b []byte) error {
	var a struct {
		File      string  `json:"file"`
		Records   int     `json:"records"`
		SizeBytes int64   `json:"sizeBytes"`
		MinLat    float64 `json:"minLat"`
		MaxLat    float64 `json:"maxLat"`
		MinLon    float64 `json:"minLon"`
		MaxLon    float64 `json:"maxLon"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	m.File, m.Records, m.SizeBytes = a.File, a.Records, a.SizeBytes
	m.MinLat, m.MaxLat, m.MinLon, m.MaxLon = a.MinLat, a.MaxLat, a.MinLon, a.MaxLon
	return nil
}

type OlexTileManifest struct {
	Version          int            `json:"version"`
	Format           string         `json:"format"`
	Name             string         `json:"name"`
	SourceFilename   string         `json:"sourceFilename"`
	SourceSHA256     string         `json:"sourceSHA256"`
	SourceSizeBytes  int64          `json:"sourceSizeBytes"`
	IndexedSizeBytes int64          `json:"indexedSizeBytes"`
	Records          int            `json:"records"`
	LatRes           float64        `json:"latRes"`
	LonRes           float64        `json:"lonRes"`
	MinLat           float64        `json:"minLat"`
	MaxLat           float64        `json:"maxLat"`
	MinLon           float64        `json:"minLon"`
	MaxLon           float64        `json:"maxLon"`
	CreatedUTC       string         `json:"createdUTC"`
	Tiles            []OlexTileMeta `json:"tiles"`
}

type countingReader struct {
	r     io.Reader
	n     int64
	lastN int64
	lastT time.Time
	on    func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.on != nil && (c.n-c.lastN >= 32<<20 || time.Since(c.lastT) >= time.Second || err == io.EOF) {
		c.lastN, c.lastT = c.n, time.Now()
		c.on(c.n)
	}
	return n, err
}

type spoolRec struct {
	LatKey int32
	LonKey int32
	Count  uint32
	Min    float32
	Sum    float64
}

type spoolHandle struct {
	file *os.File
	buf  *bufio.Writer
	used uint64
}

type tileSpooler struct {
	dir     string
	handles map[string]*spoolHandle
	clock   uint64
	files   map[string]string
}

func newTileSpooler(dir string) (*tileSpooler, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &tileSpooler{dir: dir, handles: map[string]*spoolHandle{}, files: map[string]string{}}, nil
}

func tileCoords(lat, lon float64) (int, int) {
	if lon >= 180 {
		lon = math.Nextafter(180, -math.MaxFloat64)
	}
	if lat >= 90 {
		lat = math.Nextafter(90, -math.MaxFloat64)
	}
	return int(math.Floor(lat / tileLatDegrees)), int(math.Floor(lon / tileLonDegrees))
}

func tileID(latTile, lonTile int) string {
	return fmt.Sprintf("%+04d_%+04d", latTile, lonTile)
}

func (s *tileSpooler) get(id string) (*spoolHandle, error) {
	s.clock++
	if h := s.handles[id]; h != nil {
		h.used = s.clock
		return h, nil
	}
	if len(s.handles) >= maxOpenSpools {
		var oldestID string
		oldest := ^uint64(0)
		for k, h := range s.handles {
			if h.used < oldest {
				oldest, oldestID = h.used, k
			}
		}
		if oldestID != "" {
			h := s.handles[oldestID]
			_ = h.buf.Flush()
			_ = h.file.Close()
			delete(s.handles, oldestID)
		}
	}
	path := filepath.Join(s.dir, "spool_"+id+".bin")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	h := &spoolHandle{file: f, buf: bufio.NewWriterSize(f, 1<<20), used: s.clock}
	s.handles[id] = h
	s.files[id] = path
	return h, nil
}

func (s *tileSpooler) Write(rec spoolRec, lat, lon float64) error {
	lt, ln := tileCoords(lat, lon)
	h, err := s.get(tileID(lt, ln))
	if err != nil {
		return err
	}
	var b [24]byte
	binary.LittleEndian.PutUint32(b[0:4], uint32(rec.LatKey))
	binary.LittleEndian.PutUint32(b[4:8], uint32(rec.LonKey))
	binary.LittleEndian.PutUint32(b[8:12], rec.Count)
	binary.LittleEndian.PutUint32(b[12:16], math.Float32bits(rec.Min))
	binary.LittleEndian.PutUint64(b[16:24], math.Float64bits(rec.Sum))
	_, err = h.buf.Write(b[:])
	return err
}

func (s *tileSpooler) Close() error {
	var first error
	for id, h := range s.handles {
		if err := h.buf.Flush(); err != nil && first == nil {
			first = err
		}
		if err := h.file.Close(); err != nil && first == nil {
			first = err
		}
		delete(s.handles, id)
	}
	return first
}

func (s *tileSpooler) IDs() []string {
	ids := make([]string, 0, len(s.files))
	for id := range s.files {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *tileSpooler) Path(id string) string { return s.files[id] }

func parseTileID(id string) (int, int, error) {
	parts := strings.Split(id, "_")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid tile id %q", id)
	}
	a, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, err
	}
	b, err := strconv.Atoi(parts[1])
	return a, b, err
}

func writeOlexIndexFile(path string, g *OlexGrid, source, sha string) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	gz, err := gzip.NewWriterLevel(f, gzip.BestSpeed)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriterSize(gz, 1<<20)
	if _, err = w.WriteString("OLXGRID1"); err != nil {
		return 0, err
	}
	meta := map[string]any{"name": g.Name, "source_filename": source, "sha256": sha, "record_count": len(g.List)}
	jb, _ := json.Marshal(meta)
	if err = binary.Write(w, binary.LittleEndian, uint32(len(jb))); err != nil {
		return 0, err
	}
	if _, err = w.Write(jb); err != nil {
		return 0, err
	}
	for _, v := range []float64{g.LatRes, g.LonRes, g.MinLat, g.MaxLat, g.MinLon, g.MaxLon} {
		if err = binary.Write(w, binary.LittleEndian, v); err != nil {
			return 0, err
		}
	}
	if err = binary.Write(w, binary.LittleEndian, uint32(len(g.List))); err != nil {
		return 0, err
	}
	var rec [18]byte
	for _, c := range g.List {
		binary.LittleEndian.PutUint32(rec[0:4], uint32(c.LatKey))
		binary.LittleEndian.PutUint32(rec[4:8], uint32(c.LonKey))
		binary.LittleEndian.PutUint16(rec[8:10], c.Count)
		binary.LittleEndian.PutUint32(rec[10:14], math.Float32bits(c.MinDepth))
		binary.LittleEndian.PutUint32(rec[14:18], math.Float32bits(c.MeanDepth))
		if _, err = w.Write(rec[:]); err != nil {
			return 0, err
		}
	}
	if err = w.Flush(); err != nil {
		return 0, err
	}
	if err = gz.Close(); err != nil {
		return 0, err
	}
	if err = f.Close(); err != nil {
		return 0, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	ok = true
	return st.Size(), nil
}

func parseOlexIndexFile(path string) (*OlexGrid, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return parseOlexIndexReader(gz)
}

func parseOlexIndexReader(r io.Reader) (*OlexGrid, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil {
		return nil, err
	}
	if string(magic) != "OLXGRID1" {
		return nil, fmt.Errorf("invalid OLEX index magic")
	}
	var jn uint32
	if err := binary.Read(br, binary.LittleEndian, &jn); err != nil {
		return nil, err
	}
	if jn > 16<<20 {
		return nil, fmt.Errorf("unreasonable OLEX index metadata size")
	}
	jb := make([]byte, jn)
	if _, err := io.ReadFull(br, jb); err != nil {
		return nil, err
	}
	var meta struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(jb, &meta)
	g := &OlexGrid{Name: meta.Name}
	for _, v := range []*float64{&g.LatRes, &g.LonRes, &g.MinLat, &g.MaxLat, &g.MinLon, &g.MaxLon} {
		if err := binary.Read(br, binary.LittleEndian, v); err != nil {
			return nil, err
		}
	}
	var n uint32
	if err := binary.Read(br, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n > 100_000_000 {
		return nil, fmt.Errorf("unreasonable OLEX cell count")
	}
	g.Cells = make(map[int64]OlexCell, int(n))
	g.List = make([]OlexCell, 0, int(n))
	var rec [18]byte
	for i := uint32(0); i < n; i++ {
		if _, err := io.ReadFull(br, rec[:]); err != nil {
			return nil, err
		}
		c := OlexCell{LatKey: int32(binary.LittleEndian.Uint32(rec[0:4])), LonKey: int32(binary.LittleEndian.Uint32(rec[4:8])), Count: binary.LittleEndian.Uint16(rec[8:10]), MinDepth: math.Float32frombits(binary.LittleEndian.Uint32(rec[10:14])), MeanDepth: math.Float32frombits(binary.LittleEndian.Uint32(rec[14:18]))}
		g.Cells[cellKey(c.LatKey, c.LonKey)] = c
		g.List = append(g.List, c)
	}
	return g, nil
}

func readOlexHeaderFile(path string) (olexHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return olexHeader{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return olexHeader{}, err
	}
	defer gz.Close()
	return readOlexHeaderReader(gz)
}

func readOlexHeaderReader(r io.Reader) (olexHeader, error) {
	br := bufio.NewReader(r)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil {
		return olexHeader{}, err
	}
	if string(magic) != "OLXGRID1" {
		return olexHeader{}, fmt.Errorf("not an indexed OLEX database")
	}
	var n uint32
	if err := binary.Read(br, binary.LittleEndian, &n); err != nil {
		return olexHeader{}, err
	}
	if n > 16<<20 {
		return olexHeader{}, fmt.Errorf("unreasonable OLEX metadata size")
	}
	jb := make([]byte, n)
	if _, err := io.ReadFull(br, jb); err != nil {
		return olexHeader{}, err
	}
	var meta struct {
		Name        string `json:"name"`
		RecordCount int    `json:"record_count"`
	}
	_ = json.Unmarshal(jb, &meta)
	var latRes, lonRes, minLat, maxLat, minLon, maxLon float64
	for _, v := range []*float64{&latRes, &lonRes, &minLat, &maxLat, &minLon, &maxLon} {
		if err := binary.Read(br, binary.LittleEndian, v); err != nil {
			return olexHeader{}, err
		}
	}
	var rec uint32
	if err := binary.Read(br, binary.LittleEndian, &rec); err != nil {
		return olexHeader{}, err
	}
	return olexHeader{Name: meta.Name, Records: int(rec), MinLat: minLat, MaxLat: maxLat, MinLon: minLon, MaxLon: maxLon}, nil
}

func isIndexedOlexFile(path string) bool {
	_, err := readOlexHeaderFile(path)
	return err == nil
}

func validateRawSoundingFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("OLEX gzip: %w", err)
	}
	defer gz.Close()
	sc := bufio.NewScanner(io.LimitReader(gz, 16<<20))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	checked, valid := 0, 0
	for sc.Scan() && checked < 50000 {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		checked++
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		lat, e1 := strconv.ParseFloat(f[0], 64)
		lon, e2 := strconv.ParseFloat(f[1], 64)
		dep, e3 := strconv.ParseFloat(f[2], 64)
		if e1 == nil && e2 == nil && e3 == nil && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 && dep >= 0 && !math.IsNaN(dep) && !math.IsInf(dep, 0) {
			valid++
			if valid >= 3 {
				return nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("unsupported OLEX gzip content: %w", err)
	}
	return fmt.Errorf("unsupported OLEX file format: expected a gzip text export with latitude longitude depth rows, or an .olxidx.gz index; native OLEX backups, ISO/TGZ media and split archives cannot be indexed directly")
}

func importRawToSpools(path string, total int64, sp *tileSpooler, report func(OlexImportProgress)) (latRes, lonRes, minLat, maxLat, minLon, maxLon float64, valid int64, sha string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	cr := &countingReader{r: io.TeeReader(f, h), lastT: time.Now()}
	cr.on = func(n int64) {
		if report != nil {
			report(OlexImportProgress{Phase: "reading compressed OLEX soundings", Progress: .72 * ratio(n, total), BytesRead: n, TotalBytes: total, ValidRows: valid, Detail: "Streaming directly from disk; memory use remains bounded."})
		}
	}
	gz, err := gzip.NewReader(cr)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("OLEX gzip: %w", err)
	}
	defer gz.Close()
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	latRes, lonRes = .001, .002
	minLat, maxLat = math.Inf(1), math.Inf(-1)
	minLon, maxLon = math.Inf(1), math.Inf(-1)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		lat, e1 := strconv.ParseFloat(fields[0], 64)
		lon, e2 := strconv.ParseFloat(fields[1], 64)
		dep, e3 := strconv.ParseFloat(fields[2], 64)
		if e1 != nil || e2 != nil || e3 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 || dep < 0 || math.IsNaN(dep) || math.IsInf(dep, 0) {
			continue
		}
		lk := int32(math.Round(lat / latRes))
		ok := int32(math.Round(lon / lonRes))
		if err := sp.Write(spoolRec{LatKey: lk, LonKey: ok, Count: 1, Min: float32(dep), Sum: dep}, lat, lon); err != nil {
			return 0, 0, 0, 0, 0, 0, valid, "", err
		}
		valid++
		minLat, maxLat = math.Min(minLat, lat), math.Max(maxLat, lat)
		minLon, maxLon = math.Min(minLon, lon), math.Max(maxLon, lon)
	}
	if err := sc.Err(); err != nil {
		return 0, 0, 0, 0, 0, 0, valid, "", err
	}
	if err := gz.Close(); err != nil {
		return 0, 0, 0, 0, 0, 0, valid, "", err
	}
	if valid == 0 {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("no valid OLEX depth soundings found; the file must be a supported .gz sounding export or .olxidx.gz index")
	}
	return latRes, lonRes, minLat, maxLat, minLon, maxLon, valid, hex.EncodeToString(h.Sum(nil)), nil
}

func importIndexToSpools(path string, total int64, sp *tileSpooler, report func(OlexImportProgress)) (latRes, lonRes, minLat, maxLat, minLon, maxLon float64, valid int64, sha string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	cr := &countingReader{r: io.TeeReader(f, h), lastT: time.Now()}
	cr.on = func(n int64) {
		if report != nil {
			report(OlexImportProgress{Phase: "reading existing OLEX index", Progress: .72 * ratio(n, total), BytesRead: n, TotalBytes: total, ValidRows: valid, Detail: "Re-tiling the index for corridor-only loading."})
		}
	}
	gz, err := gzip.NewReader(cr)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, 0, "", err
	}
	defer gz.Close()
	br := bufio.NewReaderSize(gz, 1<<20)
	magic := make([]byte, 8)
	if _, err = io.ReadFull(br, magic); err != nil || string(magic) != "OLXGRID1" {
		return 0, 0, 0, 0, 0, 0, 0, "", fmt.Errorf("invalid OLEX index")
	}
	var jn uint32
	if err = binary.Read(br, binary.LittleEndian, &jn); err != nil {
		return
	}
	if jn > 16<<20 {
		err = fmt.Errorf("unreasonable OLEX metadata size")
		return
	}
	if _, err = io.CopyN(io.Discard, br, int64(jn)); err != nil {
		return
	}
	for _, v := range []*float64{&latRes, &lonRes, &minLat, &maxLat, &minLon, &maxLon} {
		if err = binary.Read(br, binary.LittleEndian, v); err != nil {
			return
		}
	}
	var n uint32
	if err = binary.Read(br, binary.LittleEndian, &n); err != nil {
		return
	}
	if n > 100_000_000 {
		err = fmt.Errorf("unreasonable OLEX cell count")
		return
	}
	var rec [18]byte
	for i := uint32(0); i < n; i++ {
		if _, err = io.ReadFull(br, rec[:]); err != nil {
			return
		}
		c := OlexCell{LatKey: int32(binary.LittleEndian.Uint32(rec[0:4])), LonKey: int32(binary.LittleEndian.Uint32(rec[4:8])), Count: binary.LittleEndian.Uint16(rec[8:10]), MinDepth: math.Float32frombits(binary.LittleEndian.Uint32(rec[10:14])), MeanDepth: math.Float32frombits(binary.LittleEndian.Uint32(rec[14:18]))}
		lat, lon := float64(c.LatKey)*latRes, float64(c.LonKey)*lonRes
		if err = sp.Write(spoolRec{LatKey: c.LatKey, LonKey: c.LonKey, Count: uint32(c.Count), Min: c.MinDepth, Sum: float64(c.MeanDepth) * float64(c.Count)}, lat, lon); err != nil {
			return
		}
		valid++
	}
	if err = gz.Close(); err != nil {
		return
	}
	sha = hex.EncodeToString(h.Sum(nil))
	return
}

func ratio(n, d int64) float64 {
	if d <= 0 {
		return 0
	}
	x := float64(n) / float64(d)
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func aggregateSpool(path, name string, latRes, lonRes float64) (*OlexGrid, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	type agg struct {
		count uint64
		min   float64
		sum   float64
	}
	cells := map[int64]*agg{}
	br := bufio.NewReaderSize(f, 1<<20)
	var rec [24]byte
	for {
		_, readErr := io.ReadFull(br, rec[:])
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, readErr
		}
		r := spoolRec{LatKey: int32(binary.LittleEndian.Uint32(rec[0:4])), LonKey: int32(binary.LittleEndian.Uint32(rec[4:8])), Count: binary.LittleEndian.Uint32(rec[8:12]), Min: math.Float32frombits(binary.LittleEndian.Uint32(rec[12:16])), Sum: math.Float64frombits(binary.LittleEndian.Uint64(rec[16:24]))}
		k := cellKey(r.LatKey, r.LonKey)
		a := cells[k]
		if a == nil {
			a = &agg{min: float64(r.Min)}
			cells[k] = a
		}
		a.count += uint64(r.Count)
		a.sum += r.Sum
		if float64(r.Min) < a.min {
			a.min = float64(r.Min)
		}
	}
	if len(cells) == 0 {
		return nil, fmt.Errorf("empty OLEX tile")
	}
	g := &OlexGrid{Name: name, LatRes: latRes, LonRes: lonRes, MinLat: math.Inf(1), MaxLat: math.Inf(-1), MinLon: math.Inf(1), MaxLon: math.Inf(-1), Cells: make(map[int64]OlexCell, len(cells)), List: make([]OlexCell, 0, len(cells))}
	for k, a := range cells {
		lk := int32(k >> 32)
		ok := int32(uint32(k))
		cnt := a.count
		if cnt > 65535 {
			cnt = 65535
		}
		c := OlexCell{LatKey: lk, LonKey: ok, Count: uint16(cnt), MinDepth: float32(a.min), MeanDepth: float32(a.sum / float64(a.count))}
		g.Cells[k] = c
		g.List = append(g.List, c)
		lat, lon := float64(lk)*latRes, float64(ok)*lonRes
		g.MinLat, g.MaxLat = math.Min(g.MinLat, lat), math.Max(g.MaxLat, lat)
		g.MinLon, g.MaxLon = math.Min(g.MinLon, lon), math.Max(g.MaxLon, lon)
	}
	sort.Slice(g.List, func(i, j int) bool {
		if g.List[i].LatKey == g.List[j].LatKey {
			return g.List[i].LonKey < g.List[j].LonKey
		}
		return g.List[i].LatKey < g.List[j].LatKey
	})
	return g, nil
}

func (l *OlexLibrary) importNameAvailable(name string) error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if !l.state.BuiltinRemoved && strings.EqualFold(l.state.BuiltinName, name) {
		return fmt.Errorf("an OLEX database named %s already exists", name)
	}
	for _, a := range l.state.Imports {
		if strings.EqualFold(a.Name, name) {
			return fmt.Errorf("an OLEX database named %s already exists", name)
		}
	}
	return nil
}

func (l *OlexLibrary) ImportGZFile(name, source, sourcePath string, report func(OlexImportProgress)) (OlexAreaInfo, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		base := filepath.Base(source)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if name == "" {
		return OlexAreaInfo{}, fmt.Errorf("database name is empty")
	}
	if err := l.importNameAvailable(name); err != nil {
		return OlexAreaInfo{}, err
	}
	st, err := os.Stat(sourcePath)
	if err != nil {
		return OlexAreaInfo{}, err
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return OlexAreaInfo{}, fmt.Errorf("selected OLEX file is empty or not a regular file")
	}
	seed := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d", name, source, st.Size(), time.Now().UnixNano())))
	base := fmt.Sprintf("olex_%x.olxdb", seed[:12])
	partial := filepath.Join(l.dir, base+".partial")
	final := filepath.Join(l.dir, base)
	_ = os.RemoveAll(partial)
	if err := os.MkdirAll(filepath.Join(partial, "tiles"), 0755); err != nil {
		return OlexAreaInfo{}, err
	}
	defer func() { _ = os.RemoveAll(partial) }()
	spoolDir := filepath.Join(partial, "spool")
	sp, err := newTileSpooler(spoolDir)
	if err != nil {
		return OlexAreaInfo{}, err
	}
	if report != nil {
		report(OlexImportProgress{Phase: "validating OLEX file", Progress: 0, TotalBytes: st.Size(), Detail: "Checking gzip and index format."})
	}
	var latRes, lonRes, minLat, maxLat, minLon, maxLon float64
	var valid int64
	var sourceSHA string
	if isIndexedOlexFile(sourcePath) {
		latRes, lonRes, minLat, maxLat, minLon, maxLon, valid, sourceSHA, err = importIndexToSpools(sourcePath, st.Size(), sp, report)
	} else {
		if err = validateRawSoundingFile(sourcePath); err == nil {
			latRes, lonRes, minLat, maxLat, minLon, maxLon, valid, sourceSHA, err = importRawToSpools(sourcePath, st.Size(), sp, report)
		}
	}
	closeErr := sp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return OlexAreaInfo{}, err
	}
	ids := sp.IDs()
	if len(ids) == 0 {
		return OlexAreaInfo{}, fmt.Errorf("no geographic OLEX tiles were created")
	}
	manifest := OlexTileManifest{Version: 1, Format: tiledOlexFormat, Name: name, SourceFilename: source, SourceSHA256: sourceSHA, SourceSizeBytes: st.Size(), LatRes: latRes, LonRes: lonRes, MinLat: minLat, MaxLat: maxLat, MinLon: minLon, MaxLon: maxLon, CreatedUTC: time.Now().UTC().Format(time.RFC3339)}
	for i, id := range ids {
		if report != nil {
			report(OlexImportProgress{Phase: "building geographic OLEX tiles", Progress: .72 + .27*float64(i)/float64(len(ids)), BytesRead: st.Size(), TotalBytes: st.Size(), ValidRows: valid, TilesDone: i, TilesTotal: len(ids), Detail: "Only route-near tiles will be loaded during optimization."})
		}
		g, err := aggregateSpool(sp.Path(id), name, latRes, lonRes)
		if err != nil {
			return OlexAreaInfo{}, fmt.Errorf("tile %s: %w", id, err)
		}
		lt, ln, err := parseTileID(id)
		if err != nil {
			return OlexAreaInfo{}, err
		}
		file := fmt.Sprintf("tile_%s.olxidx.gz", id)
		path := filepath.Join(partial, "tiles", file)
		sz, err := writeOlexIndexFile(path, g, source, sourceSHA)
		if err != nil {
			return OlexAreaInfo{}, err
		}
		manifest.Tiles = append(manifest.Tiles, OlexTileMeta{File: filepath.ToSlash(filepath.Join("tiles", file)), Records: len(g.List), SizeBytes: sz, MinLat: float64(lt) * tileLatDegrees, MaxLat: float64(lt+1) * tileLatDegrees, MinLon: float64(ln) * tileLonDegrees, MaxLon: float64(ln+1) * tileLonDegrees})
		manifest.Records += len(g.List)
		manifest.IndexedSizeBytes += sz
		_ = os.Remove(sp.Path(id))
	}
	_ = os.RemoveAll(spoolDir)
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	manifestPath := filepath.Join(partial, "manifest.json")
	if err := os.WriteFile(manifestPath, mb, 0644); err != nil {
		return OlexAreaInfo{}, err
	}
	manifest.IndexedSizeBytes += int64(len(mb))
	mb, _ = json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestPath, mb, 0644); err != nil {
		return OlexAreaInfo{}, err
	}
	if err := os.Rename(partial, final); err != nil {
		return OlexAreaInfo{}, err
	}
	area := storedArea{Name: name, File: base, Format: tiledOlexFormat, Records: manifest.Records, SizeBytes: manifest.SourceSizeBytes, IndexedSizeBytes: manifest.IndexedSizeBytes, MinLat: minLat, MaxLat: maxLat, MinLon: minLon, MaxLon: maxLon}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range l.state.Imports {
		if strings.EqualFold(a.Name, name) {
			_ = os.RemoveAll(final)
			return OlexAreaInfo{}, fmt.Errorf("an OLEX database named %s already exists", name)
		}
	}
	l.state.Imports = append(l.state.Imports, area)
	if err := l.save(); err != nil {
		_ = os.RemoveAll(final)
		return OlexAreaInfo{}, err
	}
	if report != nil {
		report(OlexImportProgress{Phase: "complete", Progress: 1, BytesRead: st.Size(), TotalBytes: st.Size(), ValidRows: valid, TilesDone: len(ids), TilesTotal: len(ids), Detail: fmt.Sprintf("%d corridor-loadable tiles are ready.", len(ids))})
	}
	return OlexAreaInfo{Name: name, Records: manifest.Records, SizeBytes: manifest.SourceSizeBytes, IndexedSizeBytes: manifest.IndexedSizeBytes, Format: tiledOlexFormat, MinLat: minLat, MaxLat: maxLat, MinLon: minLon, MaxLon: maxLon}, nil
}

func loadOlexManifest(path string) (*OlexTileManifest, error) {
	b, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var m OlexTileManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Format != tiledOlexFormat || m.Version != 1 {
		return nil, fmt.Errorf("unsupported tiled OLEX manifest")
	}
	return &m, nil
}

func (l *OlexLibrary) loadTiledForBBox(area storedArea, minLat, maxLat, minLon, maxLon float64) ([]*OlexGrid, error) {
	root := filepath.Join(l.dir, area.File)
	m, err := loadOlexManifest(root)
	if err != nil {
		return nil, err
	}
	out := []*OlexGrid{}
	for _, t := range m.Tiles {
		if t.MaxLat < minLat || t.MinLat > maxLat || t.MaxLon < minLon || t.MinLon > maxLon {
			continue
		}
		g, err := parseOlexIndexFile(filepath.Join(root, filepath.FromSlash(t.File)))
		if err != nil {
			return nil, fmt.Errorf("load OLEX tile %s: %w", t.File, err)
		}
		g.Name = area.Name
		out = append(out, g)
	}
	return out, nil
}
