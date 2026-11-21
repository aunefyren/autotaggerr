package main

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// The universe is a synthetic MusicBrainz: artists, albums, recordings and the
// relationships between them, generated deterministically from a seed. The same seed
// and size always produce the same IDs, names and structure, which is what lets the
// stand-in server rebuild it in memory instead of reading fixtures from disk.
//
// Nothing here is real. MBIDs are UUIDv5 under demoNamespace, so they are stable
// across runs and cannot be confused with real MusicBrainz entities, except Various
// Artists, which uses MusicBrainz's own special-purpose ID because the app treats it
// specially (insights.specialArtists).

// demoNamespace roots every generated MBID.
var demoNamespace = uuid.MustParse("5e1f0c2a-7d3b-4b8e-9a61-0d2c4f8e7a10")

const variousArtistsID = "89ad4ac3-39f7-470e-963a-56509c546377"

// Preset is a library size.
type Preset struct {
	Name    string
	Artists int
}

// Presets, smallest first: about 300, 2k, 20k and 100k track files. A main artist
// comes to roughly 24 files once singles, compilations and duplicate copies are
// counted.
var Presets = []Preset{
	{"tiny", 10},
	{"small", 85},
	{"medium", 850},
	{"large", 4200},
}

func presetByName(name string) (Preset, bool) {
	for _, p := range Presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

type Artist struct {
	ID, Name, SortName string
	Type               string // Person or Group
	Gender, Country    string
	Begin              string
	Genre              string
	// Role is what the generator uses them for: main (has albums), producer,
	// engineer, session (instruments) or writer.
	Role       string
	Instrument string
}

type Credit struct {
	Artist *Artist
	Join   string
}

type Rel struct {
	Type       string
	Artist     *Artist
	Attributes []string
}

type Work struct {
	ID, Title string
	Rels      []Rel
}

type Recording struct {
	ID, Title string
	LengthMS  int
	Credit    []Credit
	ISRC      string
	Rels      []Rel
	Work      *Work
	FirstDate string
	// Master seeds the audio bytes, so two files of one recording are byte-identical
	// unless a holding asks for a different master.
	Master uint64
}

type Track struct {
	ID        string
	Position  int
	Recording *Recording
}

type Medium struct {
	Position int
	Format   string
	Tracks   []*Track
}

type Label struct{ ID, Name string }

type Release struct {
	ID, Title, Date, Country, Barcode, Disambiguation string
	Group                                             *ReleaseGroup
	Label                                             *Label
	CatalogNumber                                     string
	Media                                             []*Medium
	Rels                                              []Rel
}

type ReleaseGroup struct {
	ID, Title, Primary string
	Secondary          []string
	FirstDate          string
	Credit             []Credit
	Genre              string
	Releases           []*Release
}

// Holding is one copy of a release on disk: the files the generator writes.
type Holding struct {
	Release *Release
	Format  string // flac or mp3
	// Folder suffix after "<Album> (<Year>)", e.g. " [Deluxe]", " [MP3]", " (copy)".
	Suffix string
	// Salt is XORed into each recording's master; 0 keeps the original audio, so a
	// copy is byte-identical to another copy with salt 0.
	Salt uint64
}

// Universe is everything generated from one seed and preset.
type Universe struct {
	Seed     int64
	Preset   Preset
	Artists  []*Artist
	Groups   []*ReleaseGroup
	Releases []*Release
	Holdings []Holding
	// Unmatched is how many files carry no MusicBrainz IDs at all.
	Unmatched int

	artistByID  map[string]*Artist
	groupByID   map[string]*ReleaseGroup
	releaseByID map[string]*Release
	// groupsByArtist lists the release groups each artist is credited on.
	groupsByArtist map[string][]*ReleaseGroup
}

type gen struct {
	r       *rand.Rand
	u       *Universe
	counter map[string]int
	names   map[string]bool
}

// mbid makes a stable ID from a kind and a running counter.
func (g *gen) mbid(kind string) string {
	g.counter[kind]++
	return uuid.NewSHA1(demoNamespace, []byte(fmt.Sprintf("%d/%s/%d", g.u.Seed, kind, g.counter[kind]))).String()
}

var (
	firstNames = []string{"Ada", "Bram", "Cleo", "Dario", "Edda", "Felix", "Greta", "Hugo", "Ines", "Jonas", "Kaia", "Leon", "Mira", "Nils", "Olga", "Pavel", "Rosa", "Sami", "Tove", "Ugo", "Vera", "Wim", "Xena", "Yara", "Zeno", "Alma", "Birk", "Cyra", "Dag", "Elin", "Frode", "Gina", "Hanne", "Ivo", "June", "Kasper", "Liv", "Marco", "Noor", "Otto"}
	lastNames  = []string{"Aalto", "Berg", "Castell", "Dahl", "Ekberg", "Falk", "Grieg", "Holm", "Isaksen", "Juul", "Krog", "Lund", "Moe", "Nyland", "Orr", "Pihl", "Quist", "Rask", "Sand", "Tangen", "Ulv", "Vik", "Wold", "Ystad", "Zahl", "Amundsen", "Brekke", "Coll", "Dreyer", "Engen", "Fossum", "Gran", "Hauge", "Ilstad", "Jaeger", "Kvam", "Lie", "Myhre", "Nesse", "Ofte"}
	adjectives = []string{"Silver", "Hollow", "Northern", "Velvet", "Paper", "Electric", "Quiet", "Golden", "Broken", "Midnight", "Copper", "Glass", "Wild", "Lunar", "Static", "Crimson", "Distant", "Frozen", "Neon", "Secret", "Amber", "Restless", "Pale", "Burning", "Gentle"}
	nouns      = []string{"Harbour", "Foxes", "Engines", "Lanterns", "Rivers", "Signals", "Wolves", "Gardens", "Machines", "Echoes", "Satellites", "Ghosts", "Mountains", "Tides", "Sparrows", "Circuits", "Islands", "Mirrors", "Orchards", "Comets", "Embers", "Valleys", "Wires", "Horizons", "Owls"}
	titleWords = []string{"Light", "Morning", "Shadow", "Heart", "Window", "Summer", "Road", "Ocean", "Dream", "Fire", "Winter", "Stars", "Rain", "City", "Home", "Signal", "Glow", "Static", "Paper", "Wave", "Garden", "Silence", "Echo", "Velvet", "Gold", "Dust", "Bloom", "Northern", "Lights", "Satellite"}
	genres     = []string{"indie rock", "electronic", "jazz", "hip hop", "folk", "pop", "metal", "ambient", "soul", "classical", "punk", "house", "r&b", "country", "techno"}
	countries  = []string{"NO", "SE", "DK", "GB", "US", "DE", "FR", "NL", "IS", "FI", "CA", "AU", "JP"}
	instrument = []string{"bass", "drums", "guitar", "piano", "synthesizer", "violin", "cello", "saxophone", "trumpet", "keyboard", "percussion", "organ"}
)

func (g *gen) pick(list []string) string { return list[g.r.Intn(len(list))] }

// unique returns name, or name with a numeral appended when it is taken, so every
// artist folder is distinct.
func (g *gen) unique(name string) string {
	candidate := name
	for i := 2; g.names[candidate]; i++ {
		candidate = fmt.Sprintf("%s %d", name, i)
	}
	g.names[candidate] = true
	return candidate
}

func (g *gen) person(role string) *Artist {
	// A few draws, with a middle initial from the second on, before a numeral: the
	// name lists give 1,600 pairs and the large preset needs more people than that.
	first, last := g.pick(firstNames), g.pick(lastNames)
	given := first
	for try := 0; try < 4 && g.names[given+" "+last]; try++ {
		first, last = g.pick(firstNames), g.pick(lastNames)
		given = first
		if try > 0 {
			given = first + " " + string(rune('A'+g.r.Intn(26))) + "."
		}
	}
	name := g.unique(given + " " + last)
	sortName := last + ", " + given
	if name != given+" "+last {
		sortName = name
	}
	gender := "Female"
	if g.r.Intn(2) == 0 {
		gender = "Male"
	}
	a := &Artist{ID: g.mbid("artist"), Name: name, SortName: sortName, Type: "Person", Gender: gender,
		Country: g.pick(countries), Begin: fmt.Sprintf("%d", 1940+g.r.Intn(60)), Role: role, Genre: g.pick(genres)}
	g.addArtist(a)
	return a
}

func (g *gen) group() *Artist {
	draw := func() string {
		switch g.r.Intn(5) {
		case 0:
			return "The " + g.pick(adjectives) + " " + g.pick(nouns)
		case 1:
			return g.pick(adjectives) + " " + g.pick(nouns)
		case 2:
			return g.pick(nouns) + " of " + g.pick(titleWords)
		case 3:
			return g.pick(firstNames) + " & the " + g.pick(nouns)
		default:
			return g.pick(adjectives) + " " + g.pick(titleWords) + " " + g.pick(nouns)
		}
	}
	name := draw()
	for try := 0; try < 6 && g.names[name]; try++ {
		name = draw()
	}
	name = g.unique(name)
	sortName := name
	if rest, ok := strings.CutPrefix(name, "The "); ok {
		sortName = rest + ", The"
	}
	a := &Artist{ID: g.mbid("artist"), Name: name, SortName: sortName, Type: "Group",
		Country: g.pick(countries), Begin: fmt.Sprintf("%d", 1960+g.r.Intn(60)), Role: "main", Genre: g.pick(genres)}
	g.addArtist(a)
	return a
}

func (g *gen) addArtist(a *Artist) {
	g.u.Artists = append(g.u.Artists, a)
	g.u.artistByID[a.ID] = a
}

func (g *gen) title(words int) string {
	parts := make([]string, words)
	for i := range parts {
		parts[i] = g.pick(titleWords)
	}
	t := strings.Join(parts, " ")
	if g.r.Intn(5) == 0 {
		t = "The " + t
	}
	return t
}

// weighted picks from a pool with a power-law preference for the front, which is
// what turns a few producers and session players into the hubs a real library has.
func (g *gen) weighted(pool []*Artist) *Artist { return g.weightedBy(pool, 0.9) }

// weightedBy is weighted with a chosen skew: 0.9 makes a few hubs (producers), lower
// spreads the work out (session players, who otherwise ended up on half the library).
func (g *gen) weightedBy(pool []*Artist, skew float64) *Artist {
	total := 0.0
	for i := range pool {
		total += 1 / math.Pow(float64(i+1), skew)
	}
	x := g.r.Float64() * total
	for i, a := range pool {
		x -= 1 / math.Pow(float64(i+1), skew)
		if x <= 0 {
			return a
		}
	}
	return pool[len(pool)-1]
}

type pools struct {
	producers, engineers, session, writers []*Artist
	byGenre                                map[string][]*Artist
}

// GenerateUniverse builds the universe for a seed and preset.
func GenerateUniverse(seed int64, preset Preset) *Universe {
	u := &Universe{
		Seed: seed, Preset: preset,
		artistByID: map[string]*Artist{}, groupByID: map[string]*ReleaseGroup{},
		releaseByID: map[string]*Release{}, groupsByArtist: map[string][]*ReleaseGroup{},
	}
	g := &gen{r: rand.New(rand.NewSource(seed)), u: u, counter: map[string]int{}, names: map[string]bool{}}

	va := &Artist{ID: variousArtistsID, Name: "Various Artists", SortName: "Various Artists", Type: "Other", Role: "special"}
	g.addArtist(va)

	n := preset.Artists
	p := pools{byGenre: map[string][]*Artist{}}
	for i := 0; i < max(3, n/12); i++ {
		p.producers = append(p.producers, g.person("producer"))
	}
	for i := 0; i < max(2, n/20); i++ {
		p.engineers = append(p.engineers, g.person("engineer"))
	}
	for i := 0; i < max(4, n/6); i++ {
		a := g.person("session")
		a.Instrument = g.pick(instrument)
		p.session = append(p.session, a)
	}
	for i := 0; i < max(3, n/10); i++ {
		p.writers = append(p.writers, g.person("writer"))
	}

	labels := make([]*Label, max(3, n/20))
	for i := range labels {
		labels[i] = &Label{ID: g.mbid("label"), Name: g.pick(adjectives) + " " + g.pick([]string{"Records", "Recordings", "Music", "Sound", "Tapes"})}
	}

	var mains []*Artist
	for i := 0; i < n; i++ {
		var a *Artist
		if g.r.Intn(3) == 0 {
			a = g.person("main")
		} else {
			a = g.group()
		}
		mains = append(mains, a)
		p.byGenre[a.Genre] = append(p.byGenre[a.Genre], a)
	}

	var albumRecordings []*Recording
	for _, a := range mains {
		albumRecordings = append(albumRecordings, g.discography(a, mains, p, labels)...)
	}
	g.compilations(va, albumRecordings, labels, max(1, n/12))
	g.holdings(mains)

	for _, rg := range u.Groups {
		seen := map[string]bool{}
		for _, c := range rg.Credit {
			if !seen[c.Artist.ID] {
				seen[c.Artist.ID] = true
				u.groupsByArtist[c.Artist.ID] = append(u.groupsByArtist[c.Artist.ID], rg)
			}
		}
	}
	return u
}

// discography generates one main artist's albums, singles, EPs and live albums, and
// returns the album recordings (for compilations to draw on).
func (g *gen) discography(a *Artist, mains []*Artist, p pools, labels []*Label) []*Recording {
	home := []*Artist{g.weighted(p.producers)}
	if g.r.Intn(3) == 0 {
		home = append(home, g.weighted(p.producers))
	}
	engineer := g.weighted(p.engineers)
	var band []*Artist
	for i := 0; i < 2+g.r.Intn(3); i++ {
		band = append(band, g.weightedBy(p.session, 0.3))
	}
	var writers []*Artist
	if a.Type == "Group" {
		writers = append(writers, g.weighted(p.writers))
	}
	label := labels[g.r.Intn(len(labels))]

	albums := 1 + int(g.r.ExpFloat64()*2.2)
	if g.r.Intn(30) == 0 {
		albums += 6
	}
	albums = min(albums, 14)
	year := 1965 + g.r.Intn(55)

	newRecording := func(year int) *Recording {
		rec := &Recording{ID: g.mbid("recording"), Title: g.title(1 + g.r.Intn(3)), LengthMS: 150000 + g.r.Intn(180000),
			Credit: []Credit{{Artist: a}}, FirstDate: fmt.Sprintf("%d", year), Master: g.r.Uint64()}
		if g.r.Intn(8) == 0 {
			peers := p.byGenre[a.Genre]
			if g.r.Intn(5) == 0 || len(peers) < 2 {
				peers = mains
			}
			if other := peers[g.r.Intn(len(peers))]; other != a {
				rec.Credit = []Credit{{Artist: a, Join: " feat. "}, {Artist: other}}
			}
		}
		if g.r.Intn(10) < 7 {
			rec.ISRC = fmt.Sprintf("%s%s%02d%05d", a.Country, strings.ToUpper(a.ID[:3]), year%100, g.counter["recording"]%100000)
		}
		producer := home[g.r.Intn(len(home))]
		if g.r.Intn(10) == 0 {
			producer = g.weighted(p.producers)
		}
		rec.Rels = append(rec.Rels, Rel{Type: "producer", Artist: producer}, Rel{Type: "mix", Artist: engineer})
		for _, s := range band {
			if g.r.Intn(3) > 0 {
				rec.Rels = append(rec.Rels, Rel{Type: "instrument", Artist: s, Attributes: []string{s.Instrument}})
			}
		}
		work := &Work{ID: g.mbid("work"), Title: rec.Title}
		composer := a
		if a.Type == "Group" {
			composer = writers[g.r.Intn(len(writers))]
		}
		work.Rels = append(work.Rels, Rel{Type: "composer", Artist: composer})
		if g.r.Intn(4) == 0 {
			work.Rels = append(work.Rels, Rel{Type: "lyricist", Artist: g.weighted(p.writers)})
		}
		rec.Work = work
		return rec
	}

	var albumRecs []*Recording
	for i := 0; i < albums; i++ {
		var recs []*Recording
		for t := 0; t < 8+g.r.Intn(7); t++ {
			recs = append(recs, newRecording(year))
		}
		albumRecs = append(albumRecs, recs...)
		rg := g.group2(g.title(1+g.r.Intn(2)), "Album", nil, year, []Credit{{Artist: a}}, a.Genre)
		discs := 1
		if g.r.Intn(20) == 0 && len(recs) >= 10 {
			discs = 2
		}
		g.release(rg, "", year, label, recs, discs, home[0])
		if g.r.Intn(5) == 0 {
			bonus := append(append([]*Recording{}, recs...), newRecording(year), newRecording(year))
			g.release(rg, "Deluxe edition", year+g.r.Intn(3), label, bonus, discs, home[0])
		}
		for s := 0; s < g.r.Intn(3); s++ {
			lead := recs[g.r.Intn(len(recs))]
			tracks := []*Recording{lead}
			if g.r.Intn(2) == 0 {
				tracks = append(tracks, newRecording(year))
			}
			single := g.group2(lead.Title, "Single", nil, year, []Credit{{Artist: a}}, a.Genre)
			g.release(single, "", year, label, tracks, 1, nil)
		}
		year += 1 + g.r.Intn(3)
	}
	if g.r.Intn(5) == 0 {
		var recs []*Recording
		for t := 0; t < 4+g.r.Intn(3); t++ {
			recs = append(recs, newRecording(year))
		}
		ep := g.group2(g.title(2)+" EP", "EP", nil, year, []Credit{{Artist: a}}, a.Genre)
		g.release(ep, "", year, label, recs, 1, nil)
	}
	if g.r.Intn(10) == 0 {
		var recs []*Recording
		for t := 0; t < 10+g.r.Intn(6); t++ {
			recs = append(recs, newRecording(year))
		}
		live := g.group2("Live at "+g.pick(nouns), "Album", []string{"Live"}, year, []Credit{{Artist: a}}, a.Genre)
		g.release(live, "", year, label, recs, 1, nil)
	}
	return albumRecs
}

func (g *gen) group2(title, primary string, secondary []string, year int, credit []Credit, genre string) *ReleaseGroup {
	rg := &ReleaseGroup{ID: g.mbid("release-group"), Title: title, Primary: primary, Secondary: secondary,
		FirstDate: fmt.Sprintf("%d-%02d-%02d", year, 1+g.r.Intn(12), 1+g.r.Intn(28)), Credit: credit, Genre: genre}
	g.u.Groups = append(g.u.Groups, rg)
	g.u.groupByID[rg.ID] = rg
	return rg
}

func (g *gen) release(rg *ReleaseGroup, disambiguation string, year int, label *Label, recs []*Recording, discs int, producer *Artist) *Release {
	rel := &Release{ID: g.mbid("release"), Title: rg.Title, Date: fmt.Sprintf("%d-%s", year, rg.FirstDate[5:]),
		Country: g.pick(countries), Barcode: fmt.Sprintf("%012d", g.r.Int63n(1e12)), Disambiguation: disambiguation,
		Group: rg, Label: label, CatalogNumber: fmt.Sprintf("%s-%04d", strings.ToUpper(label.Name[:3]), g.r.Intn(10000))}
	if producer != nil && g.r.Intn(3) == 0 {
		rel.Rels = []Rel{{Type: "producer", Artist: producer}}
	}
	format := "Digital Media"
	if g.r.Intn(2) == 0 {
		format = "CD"
	}
	perDisc := (len(recs) + discs - 1) / discs
	for d := 0; d < discs; d++ {
		m := &Medium{Position: d + 1, Format: format}
		for i := d * perDisc; i < min(len(recs), (d+1)*perDisc); i++ {
			m.Tracks = append(m.Tracks, &Track{ID: g.mbid("track"), Position: len(m.Tracks) + 1, Recording: recs[i]})
		}
		rel.Media = append(rel.Media, m)
	}
	rg.Releases = append(rg.Releases, rel)
	g.u.Releases = append(g.u.Releases, rel)
	g.u.releaseByID[rel.ID] = rel
	return rel
}

func (g *gen) compilations(va *Artist, pool []*Recording, labels []*Label, count int) {
	for i := 0; i < count && len(pool) > 0; i++ {
		var recs []*Recording
		seen := map[*Recording]bool{}
		for t := 0; t < 12+g.r.Intn(7); t++ {
			rec := pool[g.r.Intn(len(pool))]
			if !seen[rec] {
				seen[rec] = true
				recs = append(recs, rec)
			}
		}
		year := 1990 + g.r.Intn(35)
		rg := g.group2(g.pick([]string{"The Best of ", "Essential ", "Now That's ", "Hits of "})+g.pick(genres), "Album",
			[]string{"Compilation"}, year, []Credit{{Artist: va}}, g.pick(genres))
		g.release(rg, "", year, labels[g.r.Intn(len(labels))], recs, 1, nil)
	}
}

// holdings decides what is on disk: most of each artist's albums, some singles, EPs
// and compilations, and the duplicate scenarios the Insights page exists to find.
func (g *gen) holdings(mains []*Artist) {
	u := g.u
	flac := map[*Artist]bool{}
	for _, a := range mains {
		flac[a] = g.r.Intn(5) > 0
	}
	format := func(rg *ReleaseGroup) string {
		if a := rg.Credit[0].Artist; a.Role == "special" || flac[a] {
			return "flac"
		}
		return "mp3"
	}
	for _, rg := range u.Groups {
		owned := false
		switch {
		case len(rg.Secondary) > 0 && rg.Secondary[0] == "Compilation":
			owned = g.r.Intn(2) == 0
		case rg.Primary == "Single":
			owned = g.r.Intn(5) < 2
		case rg.Primary == "EP" || (len(rg.Secondary) > 0 && rg.Secondary[0] == "Live"):
			owned = g.r.Intn(5) < 3
		default:
			owned = g.r.Intn(10) < 7
		}
		if !owned {
			continue
		}
		f := format(rg)
		pick := rg.Releases[0]
		if len(rg.Releases) > 1 && g.r.Intn(5) < 2 {
			pick = rg.Releases[1]
		}
		u.Holdings = append(u.Holdings, Holding{Release: pick, Format: f})
		if rg.Primary != "Album" || len(rg.Secondary) > 0 {
			continue
		}
		// Duplicate scenarios, at rates that leave every level present even in tiny.
		if len(rg.Releases) > 1 && g.r.Intn(4) == 0 {
			other := rg.Releases[0]
			if other == pick {
				other = rg.Releases[1]
			}
			salt := uint64(0)
			if g.r.Intn(2) == 0 {
				salt = g.r.Uint64() | 1 // a remaster: same recordings, different audio
			}
			u.Holdings = append(u.Holdings, Holding{Release: other, Format: f, Suffix: " [" + strings.ToLower(other.DisambiguationOr("original")) + "]", Salt: salt})
		}
		if g.r.Intn(25) == 0 {
			alt := "mp3"
			if f == "mp3" {
				alt = "flac"
			}
			u.Holdings = append(u.Holdings, Holding{Release: pick, Format: alt, Suffix: " [" + strings.ToUpper(alt) + "]"})
		}
		if g.r.Intn(60) == 0 {
			u.Holdings = append(u.Holdings, Holding{Release: pick, Format: f, Suffix: " (copy)"})
		}
	}
	u.Unmatched = max(2, len(u.Holdings)/150)
}

// DisambiguationOr is the release's disambiguation, or fallback when it has none.
func (r *Release) DisambiguationOr(fallback string) string {
	if r.Disambiguation == "" {
		return fallback
	}
	return r.Disambiguation
}

// Year is the release's year, from its date.
func (r *Release) Year() string { return r.Date[:4] }

// Tracks counts the release's tracks.
func (r *Release) Tracks() int {
	n := 0
	for _, m := range r.Media {
		n += len(m.Tracks)
	}
	return n
}

// TrackFiles is how many audio files the holdings write (excluding unmatched ones).
func (u *Universe) TrackFiles() int {
	n := 0
	for _, h := range u.Holdings {
		n += h.Release.Tracks()
	}
	return n
}

// CreditName renders an artist credit the way MusicBrainz does.
func CreditName(credit []Credit) string {
	var b strings.Builder
	for _, c := range credit {
		b.WriteString(c.Artist.Name)
		b.WriteString(c.Join)
	}
	return b.String()
}

// sortedArtists returns artists by name, for stable search results.
func (u *Universe) sortedArtists() []*Artist {
	out := append([]*Artist{}, u.Artists...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
