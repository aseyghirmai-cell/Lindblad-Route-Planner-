package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type OlexContribution struct {
	Name    string  `json:"name"`
	Percent float64 `json:"percent"`
}
type OlexAreaInfo struct {
	Name             string  `json:"name"`
	Records          int     `json:"records"`
	Builtin          bool    `json:"builtin"`
	Disabled         bool    `json:"disabled"`
	SizeBytes        int64   `json:"sizeBytes"`
	IndexedSizeBytes int64   `json:"indexedSizeBytes,omitempty"`
	Format           string  `json:"format,omitempty"`
	MinLat           float64 `json:"minLat"`
	MaxLat           float64 `json:"maxLat"`
	MinLon           float64 `json:"minLon"`
	MaxLon           float64 `json:"maxLon"`
}
type storedArea struct {
	Name                           string `json:"name"`
	File                           string `json:"file"`
	Format                         string `json:"format,omitempty"`
	Disabled                       bool   `json:"disabled"`
	Records                        int    `json:"records"`
	SizeBytes                      int64  `json:"sizeBytes"`
	IndexedSizeBytes               int64  `json:"indexedSizeBytes,omitempty"`
	MinLat, MaxLat, MinLon, MaxLon float64
}
type libraryState struct {
	BuiltinName     string       `json:"builtinName"`
	BuiltinDisabled bool         `json:"builtinDisabled"`
	BuiltinRemoved  bool         `json:"builtinRemoved"`
	Imports         []storedArea `json:"imports"`
}
type OlexLibrary struct {
	mu       sync.RWMutex
	dir      string
	state    libraryState
	embedded []byte
	cache    map[string]*OlexGrid
}

type OlexCell struct {
	LatKey, LonKey      int32
	Count               uint16
	MinDepth, MeanDepth float32
}
type OlexGrid struct {
	Name                           string
	LatRes, LonRes                 float64
	MinLat, MaxLat, MinLon, MaxLon float64
	Cells                          map[int64]OlexCell
	List                           []OlexCell
}

func cellKey(a, b int32) int64 { return int64(a)<<32 | int64(uint32(b)) }

type OlexSupportSegment struct {
	StartFraction float64 `json:"startFraction"`
	EndFraction   float64 `json:"endFraction"`
	Status        string  `json:"status"`
}

type OlexAssessment struct {
	Status                                                 string
	SupportedFraction, ReviewFraction, UnsupportedFraction float64
	PrimaryArea, Comment                                   string
	AreaFractions                                          map[string]float64
	Segments                                               []OlexSupportSegment
}
type OlexPreviewCell struct {
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	MeanDepth float64 `json:"meanDepth"`
}
type CompositeOlex struct{ Grids []*OlexGrid }

func NewOlexLibrary(dir string, embedded []byte) (*OlexLibrary, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	l := &OlexLibrary{dir: dir, embedded: embedded, cache: map[string]*OlexGrid{}}
	l.state.BuiltinName = "Antarctic Peninsula"
	b, err := os.ReadFile(filepath.Join(dir, "olex_library.json"))
	if err == nil {
		_ = json.Unmarshal(b, &l.state)
	}
	if l.state.BuiltinName == "" {
		l.state.BuiltinName = "Antarctic Peninsula"
	}
	l.cleanMissing()
	l.migrateLegacyIndexes()
	_ = l.save()
	return l, nil
}

func (l *OlexLibrary) migrateLegacyIndexes() {
	marker := filepath.Join(l.dir, ".legacy_migration_done")
	if _, err := os.Stat(marker); err == nil {
		return
	}
	roots := []string{}
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		roots = append(roots,
			filepath.Join(v, "LindbladRoutePlannerOnlineData"),
			filepath.Join(v, "LindbladRoutePlannerManaged"),
			filepath.Join(v, "LindbladRoutePlannerData"))
	}
	if v := os.Getenv("USERPROFILE"); v != "" {
		roots = append(roots, filepath.Join(v, "LindbladRoutePlannerOnlineData"))
	}
	if exe, err := os.Executable(); err == nil {
		base := filepath.Dir(exe)
		roots = append(roots, filepath.Join(base, "LindbladRoutePlannerOnlineData"), filepath.Join(base, "LindbladRoutePlannerManaged"))
	}
	existing := map[string]bool{strings.ToLower(l.state.BuiltinName): true}
	for _, a := range l.state.Imports {
		existing[strings.ToLower(a.Name)] = true
	}
	migrated := 0
	for _, root := range roots {
		root = filepath.Clean(root)
		if root == filepath.Clean(l.dir) {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || migrated >= 100 {
				return nil
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(root, path)
				if strings.Count(rel, string(os.PathSeparator)) > 5 || filepath.Clean(path) == filepath.Clean(l.dir) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".olxidx.gz") {
				return nil
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return nil
			}
			h, e := readOlexHeader(b)
			if e != nil || h.Records == 0 {
				return nil
			}
			name := strings.TrimSpace(h.Name)
			if name == "" {
				name = strings.TrimSuffix(strings.TrimSuffix(d.Name(), ".gz"), ".olxidx")
			}
			baseName := name
			for n := 2; existing[strings.ToLower(name)]; n++ {
				if strings.EqualFold(name, l.state.BuiltinName) {
					return nil
				}
				name = fmt.Sprintf("%s (%d)", baseName, n)
			}
			sha := sha256.Sum256([]byte(path))
			file := fmt.Sprintf("migrated_%x.olxidx.gz", sha[:10])
			if e = os.WriteFile(filepath.Join(l.dir, file), b, 0644); e != nil {
				return nil
			}
			l.state.Imports = append(l.state.Imports, storedArea{Name: name, File: file, Records: h.Records, SizeBytes: int64(len(b)), MinLat: h.MinLat, MaxLat: h.MaxLat, MinLon: h.MinLon, MaxLon: h.MaxLon})
			existing[strings.ToLower(name)] = true
			migrated++
			return nil
		})
	}
	_ = os.WriteFile(marker, []byte(fmt.Sprintf("migrated=%d\n", migrated)), 0644)
}

func (l *OlexLibrary) cleanMissing() {
	out := l.state.Imports[:0]
	for _, a := range l.state.Imports {
		if _, err := os.Stat(filepath.Join(l.dir, a.File)); err == nil {
			out = append(out, a)
		}
	}
	l.state.Imports = out
}
func (l *OlexLibrary) save() error {
	b, _ := json.MarshalIndent(l.state, "", "  ")
	tmp := filepath.Join(l.dir, "olex_library.json.tmp")
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(l.dir, "olex_library.json"))
}
func (l *OlexLibrary) Areas() []OlexAreaInfo {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := []OlexAreaInfo{}
	if !l.state.BuiltinRemoved {
		meta, _ := readOlexHeader(l.embedded)
		out = append(out, OlexAreaInfo{Name: l.state.BuiltinName, Records: meta.Records, Builtin: true, Disabled: l.state.BuiltinDisabled, SizeBytes: int64(len(l.embedded)), MinLat: meta.MinLat, MaxLat: meta.MaxLat, MinLon: meta.MinLon, MaxLon: meta.MaxLon})
	}
	for _, a := range l.state.Imports {
		out = append(out, OlexAreaInfo{Name: a.Name, Records: a.Records, Disabled: a.Disabled, SizeBytes: a.SizeBytes, IndexedSizeBytes: a.IndexedSizeBytes, Format: a.Format, MinLat: a.MinLat, MaxLat: a.MaxLat, MinLon: a.MinLon, MaxLon: a.MaxLon})
	}
	return out
}
func (l *OlexLibrary) Rename(name, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("new database name is empty")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range l.state.Imports {
		if strings.EqualFold(a.Name, newName) && !strings.EqualFold(name, newName) {
			return fmt.Errorf("an OLEX database named %s already exists", newName)
		}
	}
	if !l.state.BuiltinRemoved && strings.EqualFold(l.state.BuiltinName, newName) && !strings.EqualFold(name, newName) {
		return fmt.Errorf("an OLEX database named %s already exists", newName)
	}
	if !l.state.BuiltinRemoved && strings.EqualFold(l.state.BuiltinName, name) {
		delete(l.cache, l.state.BuiltinName)
		l.state.BuiltinName = newName
		return l.save()
	}
	for i := range l.state.Imports {
		if strings.EqualFold(l.state.Imports[i].Name, name) {
			delete(l.cache, l.state.Imports[i].Name)
			l.state.Imports[i].Name = newName
			return l.save()
		}
	}
	return fmt.Errorf("OLEX database not found: %s", name)
}
func (l *OlexLibrary) Toggle(name string, enabled bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.state.BuiltinRemoved && strings.EqualFold(l.state.BuiltinName, name) {
		l.state.BuiltinDisabled = !enabled
		return l.save()
	}
	for i := range l.state.Imports {
		if strings.EqualFold(l.state.Imports[i].Name, name) {
			l.state.Imports[i].Disabled = !enabled
			return l.save()
		}
	}
	return fmt.Errorf("OLEX database not found: %s", name)
}
func (l *OlexLibrary) Remove(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.state.BuiltinRemoved && strings.EqualFold(l.state.BuiltinName, name) {
		l.state.BuiltinRemoved = true
		delete(l.cache, l.state.BuiltinName)
		return l.save()
	}
	for i, a := range l.state.Imports {
		if strings.EqualFold(a.Name, name) {
			_ = os.RemoveAll(filepath.Join(l.dir, a.File))
			delete(l.cache, a.Name)
			l.state.Imports = append(l.state.Imports[:i], l.state.Imports[i+1:]...)
			return l.save()
		}
	}
	return fmt.Errorf("OLEX database not found: %s", name)
}

func bboxIntersects(a OlexAreaInfo, minLat, maxLat, minLon, maxLon float64) bool {
	return !(a.MaxLat < minLat || a.MinLat > maxLat || a.MaxLon < minLon || a.MinLon > maxLon)
}
func (l *OlexLibrary) AreasForCorridor(aLat, aLon, bLat, bLon float64) []OlexAreaInfo {
	minLat, maxLat := math.Min(aLat, bLat)-1, math.Max(aLat, bLat)+1
	minLon, maxLon := math.Min(aLon, bLon)-2, math.Max(aLon, bLon)+2
	out := []OlexAreaInfo{}
	for _, a := range l.Areas() {
		if !a.Disabled && bboxIntersects(a, minLat, maxLat, minLon, maxLon) {
			out = append(out, a)
		}
	}
	return out
}
func (l *OlexLibrary) CompositeForCorridor(aLat, aLon, bLat, bLon float64) (*CompositeOlex, error) {
	minLat, maxLat := math.Min(aLat, bLat)-1, math.Max(aLat, bLat)+1
	minLon, maxLon := math.Min(aLon, bLon)-2, math.Max(aLon, bLon)+2
	infos := l.AreasForCorridor(aLat, aLon, bLat, bLon)
	c := &CompositeOlex{}
	for _, info := range infos {
		l.mu.RLock()
		var stored *storedArea
		for i := range l.state.Imports {
			if strings.EqualFold(l.state.Imports[i].Name, info.Name) {
				copy := l.state.Imports[i]
				stored = &copy
				break
			}
		}
		l.mu.RUnlock()
		if stored != nil && stored.Format == tiledOlexFormat {
			grids, err := l.loadTiledForBBox(*stored, minLat, maxLat, minLon, maxLon)
			if err != nil {
				return nil, err
			}
			c.Grids = append(c.Grids, grids...)
			continue
		}
		g, err := l.Load(info.Name)
		if err != nil {
			return nil, err
		}
		c.Grids = append(c.Grids, g)
	}
	return c, nil
}
func (l *OlexLibrary) Load(name string) (*OlexGrid, error) {
	l.mu.RLock()
	if g := l.cache[name]; g != nil {
		l.mu.RUnlock()
		return g, nil
	}
	var data []byte
	var path string
	if !l.state.BuiltinRemoved && strings.EqualFold(l.state.BuiltinName, name) {
		data = l.embedded
	} else {
		for _, a := range l.state.Imports {
			if strings.EqualFold(a.Name, name) {
				path = filepath.Join(l.dir, a.File)
				break
			}
		}
	}
	l.mu.RUnlock()
	var err error
	var g *OlexGrid
	if data == nil {
		if path == "" {
			return nil, fmt.Errorf("OLEX database not found: %s", name)
		}
		g, err = parseOlexIndexFile(path)
	} else {
		g, err = ParseOlexIndex(data)
	}
	if err != nil {
		return nil, err
	}
	g.Name = name
	l.mu.Lock()
	l.cache[name] = g
	l.mu.Unlock()
	return g, nil
}

type olexHeader struct {
	Name                           string
	Records                        int
	MinLat, MaxLat, MinLon, MaxLon float64
}

func readOlexHeader(compressed []byte) (olexHeader, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return olexHeader{}, err
	}
	defer gz.Close()
	r := bufio.NewReader(gz)
	magic := make([]byte, 8)
	if _, err = io.ReadFull(r, magic); err != nil {
		return olexHeader{}, err
	}
	if string(magic) != "OLXGRID1" {
		return olexHeader{}, fmt.Errorf("not an indexed OLEX database")
	}
	var n uint32
	if err = binary.Read(r, binary.LittleEndian, &n); err != nil {
		return olexHeader{}, err
	}
	jb := make([]byte, n)
	if _, err = io.ReadFull(r, jb); err != nil {
		return olexHeader{}, err
	}
	var meta struct {
		Name        string `json:"name"`
		RecordCount int    `json:"record_count"`
	}
	_ = json.Unmarshal(jb, &meta)
	var latRes, lonRes, minLat, maxLat, minLon, maxLon float64
	for _, v := range []*float64{&latRes, &lonRes, &minLat, &maxLat, &minLon, &maxLon} {
		if err = binary.Read(r, binary.LittleEndian, v); err != nil {
			return olexHeader{}, err
		}
	}
	var rec uint32
	if err = binary.Read(r, binary.LittleEndian, &rec); err != nil {
		return olexHeader{}, err
	}
	return olexHeader{Name: meta.Name, Records: int(rec), MinLat: minLat, MaxLat: maxLat, MinLon: minLon, MaxLon: maxLon}, nil
}
func ParseOlexIndex(compressed []byte) (*OlexGrid, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	r := bufio.NewReaderSize(gz, 1<<20)
	magic := make([]byte, 8)
	if _, err = io.ReadFull(r, magic); err != nil {
		return nil, err
	}
	if string(magic) != "OLXGRID1" {
		return nil, fmt.Errorf("invalid OLEX index magic")
	}
	var jn uint32
	if err = binary.Read(r, binary.LittleEndian, &jn); err != nil {
		return nil, err
	}
	jb := make([]byte, jn)
	if _, err = io.ReadFull(r, jb); err != nil {
		return nil, err
	}
	var meta struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(jb, &meta)
	g := &OlexGrid{Name: meta.Name}
	for _, v := range []*float64{&g.LatRes, &g.LonRes, &g.MinLat, &g.MaxLat, &g.MinLon, &g.MaxLon} {
		if err = binary.Read(r, binary.LittleEndian, v); err != nil {
			return nil, err
		}
	}
	var n uint32
	if err = binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n > 100_000_000 {
		return nil, fmt.Errorf("unreasonable OLEX cell count")
	}
	g.Cells = make(map[int64]OlexCell, n)
	g.List = make([]OlexCell, 0, n)
	for i := uint32(0); i < n; i++ {
		var c OlexCell
		if err = binary.Read(r, binary.LittleEndian, &c.LatKey); err != nil {
			return nil, err
		}
		if err = binary.Read(r, binary.LittleEndian, &c.LonKey); err != nil {
			return nil, err
		}
		if err = binary.Read(r, binary.LittleEndian, &c.Count); err != nil {
			return nil, err
		}
		if err = binary.Read(r, binary.LittleEndian, &c.MinDepth); err != nil {
			return nil, err
		}
		if err = binary.Read(r, binary.LittleEndian, &c.MeanDepth); err != nil {
			return nil, err
		}
		g.Cells[cellKey(c.LatKey, c.LonKey)] = c
		g.List = append(g.List, c)
	}
	return g, nil
}

type aggCell struct {
	count    uint32
	min, sum float64
}

func (l *OlexLibrary) ImportGZ(name, source string, compressed []byte) (OlexAreaInfo, error) {
	tmp, err := os.CreateTemp(l.dir, "olex-upload-*.gz")
	if err != nil {
		return OlexAreaInfo{}, err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err = tmp.Write(compressed); err != nil {
		_ = tmp.Close()
		return OlexAreaInfo{}, err
	}
	if err = tmp.Close(); err != nil {
		return OlexAreaInfo{}, err
	}
	return l.ImportGZFile(name, source, path, nil)
}

func parseRawSoundingGZ(name string, compressed []byte) (*OlexGrid, error) {
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("OLEX gzip: %w", err)
	}
	defer gz.Close()
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	cells := map[int64]*aggCell{}
	minLat, maxLat := math.Inf(1), math.Inf(-1)
	minLon, maxLon := math.Inf(1), math.Inf(-1)
	valid := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		lat, e1 := strconv.ParseFloat(f[0], 64)
		lon, e2 := strconv.ParseFloat(f[1], 64)
		dep, e3 := strconv.ParseFloat(f[2], 64)
		if e1 != nil || e2 != nil || e3 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 || dep < 0 || math.IsNaN(dep) || math.IsInf(dep, 0) {
			continue
		}
		lk := int32(math.Round(lat / .001))
		ok := int32(math.Round(lon / .002))
		k := cellKey(lk, ok)
		a := cells[k]
		if a == nil {
			a = &aggCell{min: dep}
			cells[k] = a
		}
		a.count++
		a.sum += dep
		if dep < a.min {
			a.min = dep
		}
		valid++
		if lat < minLat {
			minLat = lat
		}
		if lat > maxLat {
			maxLat = lat
		}
		if lon < minLon {
			minLon = lon
		}
		if lon > maxLon {
			maxLon = lon
		}
	}
	if err = sc.Err(); err != nil {
		return nil, err
	}
	if valid == 0 {
		return nil, fmt.Errorf("no valid OLEX depth soundings found")
	}
	g := &OlexGrid{Name: name, LatRes: .001, LonRes: .002, MinLat: minLat, MaxLat: maxLat, MinLon: minLon, MaxLon: maxLon, Cells: make(map[int64]OlexCell, len(cells)), List: make([]OlexCell, 0, len(cells))}
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
	}
	sort.Slice(g.List, func(i, j int) bool {
		if g.List[i].LatKey == g.List[j].LatKey {
			return g.List[i].LonKey < g.List[j].LonKey
		}
		return g.List[i].LatKey < g.List[j].LatKey
	})
	return g, nil
}
func EncodeOlexIndex(g *OlexGrid, source, sha string) ([]byte, error) {
	var raw bytes.Buffer
	raw.WriteString("OLXGRID1")
	meta := map[string]any{"name": g.Name, "source_filename": source, "sha256": sha, "record_count": len(g.Cells)}
	jb, _ := json.Marshal(meta)
	_ = binary.Write(&raw, binary.LittleEndian, uint32(len(jb)))
	raw.Write(jb)
	for _, v := range []float64{g.LatRes, g.LonRes, g.MinLat, g.MaxLat, g.MinLon, g.MaxLon} {
		_ = binary.Write(&raw, binary.LittleEndian, v)
	}
	_ = binary.Write(&raw, binary.LittleEndian, uint32(len(g.List)))
	for _, c := range g.List {
		_ = binary.Write(&raw, binary.LittleEndian, c.LatKey)
		_ = binary.Write(&raw, binary.LittleEndian, c.LonKey)
		_ = binary.Write(&raw, binary.LittleEndian, c.Count)
		_ = binary.Write(&raw, binary.LittleEndian, c.MinDepth)
		_ = binary.Write(&raw, binary.LittleEndian, c.MeanDepth)
	}
	var out bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	if _, err := gz.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (g *OlexGrid) Nearest(lat, lon, maxNM float64) (OlexCell, float64, bool) {
	if g == nil || lat < g.MinLat-.02 || lat > g.MaxLat+.02 || lon < g.MinLon-.04 || lon > g.MaxLon+.04 {
		return OlexCell{}, 0, false
	}
	baseA := int32(math.Round(lat / g.LatRes))
	baseB := int32(math.Round(lon / g.LonRes))
	ra := int(math.Ceil(maxNM/(60*g.LatRes))) + 1
	c := math.Abs(math.Cos(lat * math.Pi / 180))
	if c < .05 {
		c = .05
	}
	rb := int(math.Ceil(maxNM/(60*g.LonRes*c))) + 1
	best := math.Inf(1)
	var cell OlexCell
	for da := -ra; da <= ra; da++ {
		for db := -rb; db <= rb; db++ {
			x, ok := g.Cells[cellKey(baseA+int32(da), baseB+int32(db))]
			if !ok {
				continue
			}
			d := HaversineNM(lat, lon, float64(x.LatKey)*g.LatRes, float64(x.LonKey)*g.LonRes)
			if d < best {
				best = d
				cell = x
			}
		}
	}
	return cell, best, best <= maxNM
}
func (c *CompositeOlex) AssessSegment(aLat, aLon, bLat, bLon, draft float64) OlexAssessment {
	a := OlexAssessment{Status: "OFFICER CHECK", AreaFractions: map[string]float64{}}
	if c == nil || len(c.Grids) == 0 {
		a.ReviewFraction = 1
		a.Segments = []OlexSupportSegment{{StartFraction: 0, EndFraction: 1, Status: "OFFICER CHECK"}}
		a.Comment = "NO OLEX COVERAGE: Stored depth support is unavailable for this leg."
		return a
	}
	d := HaversineNM(aLat, aLon, bLat, bLon)
	n := int(math.Ceil(d / .08))
	if n < 2 {
		n = 2
	}
	covered, critical := 0, 0
	areas := map[string]int{}
	sampleStatuses := make([]string, n+1)
	for i := 0; i <= n; i++ {
		lat, lon := InterpolateGC(aLat, aLon, bLat, bLon, float64(i)/float64(n))
		best := math.Inf(1)
		var bc OlexCell
		bn := ""
		for _, g := range c.Grids {
			cell, dist, ok := g.Nearest(lat, lon, .16)
			if ok && dist < best {
				best = dist
				bc = cell
				bn = g.Name
			}
		}
		if bn == "" {
			sampleStatuses[i] = "OFFICER CHECK"
			continue
		}
		covered++
		areas[bn]++
		if float64(bc.MinDepth) < draft+1.0 {
			critical++
			sampleStatuses[i] = "UNSUPPORTED"
		} else {
			sampleStatuses[i] = "SUPPORTED"
		}
	}
	samples := n + 1
	a.SupportedFraction = float64(covered-critical) / float64(samples)
	a.UnsupportedFraction = float64(critical) / float64(samples)
	a.ReviewFraction = math.Max(0, 1-a.SupportedFraction-a.UnsupportedFraction)

	// Preserve the exact sample fractions used by the Route Summary as ordered
	// sub-segments. The preview can then draw the same assessment instead of
	// colouring an entire leg red or amber because of one isolated sample.
	for i, status := range sampleStatuses {
		start := float64(i) / float64(samples)
		end := float64(i+1) / float64(samples)
		last := len(a.Segments) - 1
		if last >= 0 && a.Segments[last].Status == status {
			a.Segments[last].EndFraction = end
		} else {
			a.Segments = append(a.Segments, OlexSupportSegment{StartFraction: start, EndFraction: end, Status: status})
		}
	}

	maxArea := 0
	for n, k := range areas {
		a.AreaFractions[n] = float64(k) / float64(samples)
		if k > maxArea {
			maxArea = k
			a.PrimaryArea = n
		}
	}
	switch {
	case critical > 0:
		a.Status = "UNSUPPORTED"
		a.Comment = fmt.Sprintf("CRITICAL REVIEW: %.0f%% of sampled positions indicate less than %.1f m depth margin above draft.", 100*a.UnsupportedFraction, 1.0)
	case covered == samples:
		a.Status = "SUPPORTED"
		a.Comment = "OLEX SUPPORTED: Stored depth coverage is available along the sampled leg."
	default:
		a.Status = "OFFICER CHECK"
		a.Comment = fmt.Sprintf("PARTIAL OLEX: Approximately %.0f%% of sampled positions have stored depth support.", 100*float64(covered)/float64(samples))
	}
	return a
}
func (c *CompositeOlex) Preview(minLat, maxLat, minLon, maxLon float64, limit int) []OlexPreviewCell {
	if limit <= 0 {
		limit = 5000
	}
	all := []OlexPreviewCell{}
	for _, g := range c.Grids {
		for _, x := range g.List {
			lat := float64(x.LatKey) * g.LatRes
			lon := float64(x.LonKey) * g.LonRes
			if lat >= minLat && lat <= maxLat && lon >= minLon && lon <= maxLon {
				all = append(all, OlexPreviewCell{lat, lon, float64(x.MeanDepth)})
			}
		}
	}
	if len(all) <= limit {
		return all
	}
	stride := int(math.Ceil(float64(len(all)) / float64(limit)))
	out := make([]OlexPreviewCell, 0, limit)
	for i := 0; i < len(all); i += stride {
		out = append(out, all[i])
	}
	return out
}
