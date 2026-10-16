package sounds

import (
	"sort"
	"strings"
)

// SeedID1 is the id of the first code seed; EnsureSeed applies every code seed whose id is absent as one version
// (C93.2). A new facet value or a changed mapping needs a new seed id, never a silent edit: seed_v1.golden.json fails.
const SeedID1 = "seed/1"

// SeedTerm is one seed mapping.
type SeedTerm struct {
	Term  string `json:"term"`
	Facet string `json:"facet"`
	Value string `json:"value"`
}

// Seed is one code seed.
type Seed struct {
	ID    string
	Terms []SeedTerm
}

// Seeds are the code seeds in order.
func Seeds() []Seed { return []Seed{{ID: SeedID1, Terms: SeedV1()}} }

type spec struct {
	facet, value string
	terms        string // comma separated; stored through NormTerm
}

// seedV1Spec is the table (term -> facet -> value) of seed/1. Singular, plural and variant forms are separate terms
// because matching is whole-word. A term may appear under several facets (synthpop is pop and electronic).
var seedV1Spec = []spec{
	{"genre_family", "pop", "pop, pop music, synthpop, synth pop, dance pop, indie pop, electropop, electro pop, power pop, teen pop, k pop, europop"},
	{"genre_family", "rock", "rock, rock and roll, rock n roll, hard rock, indie rock, alternative rock, alt rock, classic rock, soft rock, garage rock, psychedelic rock, prog rock, progressive rock, grunge, folk rock, blues rock"},
	{"genre_family", "hip-hop", "hip hop, hiphop, rap music, trap, boom bap, drill, trip hop, lo fi hip hop, rap, rapping, rapper"},
	{"genre_family", "electronic", "electronic, electronica, edm, synthpop, synth pop, techno, idm, synthwave, electro, dubstep, drum and bass, dnb, lofi, lo fi, chiptune, vaporwave, electropop, electro pop"},
	{"genre_family", "dance", "dance, eurodance, italo disco, house, deep house, disco, club, trance, nu disco, dancefloor, euro house, dance pop"},
	{"genre_family", "reggae", "reggae, roots reggae, dub, ska, rocksteady, dancehall, reggae fusion, lovers rock"},
	{"genre_family", "funk-soul", "funk, soul, neo soul, motown, funky, northern soul, gospel, soulful, funk soul"},
	{"genre_family", "r-and-b", "r and b, rnb, r&b, rhythm and blues, contemporary r and b, slow jam"},
	{"genre_family", "jazz", "jazz, smooth jazz, bebop, swing, big band, jazz fusion, cool jazz, vocal jazz, jazzy, latin jazz"},
	{"genre_family", "blues", "blues, delta blues, chicago blues, twelve bar blues, 12 bar blues, blues rock"},
	{"genre_family", "folk", "folk, acoustic guitar, indie folk, folk rock, bluegrass, acoustic, fingerstyle, americana, celtic"},
	{"genre_family", "country", "country, country pop, outlaw country, honky tonk, nashville, western, alt country"},
	{"genre_family", "classical", "classical, orchestral, symphony, symphonic, chamber music, piano solo, baroque, opera, string quartet, concerto"},
	{"genre_family", "latin", "latin, salsa, bossa nova, samba, bachata, reggaeton, cumbia, tango, merengue, flamenco, latin pop, mariachi, latin jazz"},
	{"genre_family", "world", "world, world music, afrobeat, afrobeats, highlife, tribal, balkan, klezmer, bollywood, gamelan"},
	{"genre_family", "metal", "metal, heavy metal, death metal, thrash, black metal, doom metal, power metal, metalcore, nu metal"},
	{"genre_family", "punk", "punk, punk rock, pop punk, hardcore, post punk, ska punk, emo"},
	{"genre_family", "ambient", "ambient, ambience, drone, soundscape, atmospheric, new age, meditation, ambient music"},
	{"genre_family", "cinematic", "cinematic, soundtrack, film score, epic, trailer music, score, movie soundtrack"},
	{"genre_family", "singer-songwriter", "singer songwriter, singer songwriter acoustic, troubadour, confessional, solo acoustic"},

	{"era", "50s", "50s, fifties, 1950s"},
	{"era", "60s", "60s, sixties, 1960s"},
	{"era", "70s", "70s, seventies, 1970s"},
	{"era", "80s", "80s, eighties, 1980s"},
	{"era", "90s", "90s, nineties, 1990s"},
	{"era", "00s", "00s, noughties, 2000s, aughts"},
	{"era", "10s", "10s, 2010s"},
	{"era", "20s", "20s, 2020s"},

	{"vocal", "male", "male vocal, male vocals, male vocalist, male singer, male voice, man singing, baritone, tenor, deep male voice"},
	{"vocal", "female", "female vocal, female vocals, female vocalist, female singer, female voice, soprano, alto, woman singing"},
	{"vocal", "duet", "duet, male female duet, vocal duet, call and response"},
	{"vocal", "choir", "choir, choral, chorus, gospel choir, backing vocals"},
	{"vocal", "rap", "rap, rapping, rapper, male rap, female rap"},
	{"vocal", "instrumental", "instrumental, no vocals, no vocal, without vocals"},

	{"mood", "upbeat", "upbeat, uplifting, cheerful, happy, bright, feel good, joyful, sunny"},
	{"mood", "chill", "chill, chillout, relaxed, laid back, mellow, calm, easy listening, lounge, soothing"},
	{"mood", "melancholic", "melancholic, melancholy, sad, wistful, sorrowful, bittersweet"},
	{"mood", "romantic", "romantic, love song, sensual, intimate, tender, passionate"},
	{"mood", "dark", "dark, moody, brooding, ominous, gloomy, haunting, sinister, eerie"},
	{"mood", "dreamy", "dreamy, ethereal, hazy, floaty, lush, airy"},
	{"mood", "energetic", "energetic, driving, high energy, powerful, anthemic, pumping, lively, dynamic"},
	{"mood", "nostalgic", "nostalgic, retro, vintage, throwback, old school, sentimental"},
	{"mood", "aggressive", "aggressive, angry, intense, fierce, raw, heavy"},
	{"mood", "warm", "warm, cozy, rich, smooth, gentle"},

	{"language", "en", "english"},
	{"language", "it", "italian, italiano"},
	{"language", "es", "spanish, espanol"},
	{"language", "fr", "french, francais"},
	{"language", "de", "german, deutsch"},
	{"language", "pt", "portuguese, portugu" + "es"},
}

// SeedV1 returns the effective rows of seed/1: terms normalised through NormTerm, ordered by facet, value, term.
func SeedV1() []SeedTerm {
	var out []SeedTerm
	for _, s := range seedV1Spec {
		for _, t := range strings.Split(s.terms, ",") {
			if t = NormTerm(t); t != "" {
				out = append(out, SeedTerm{Term: t, Facet: s.facet, Value: s.value})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Facet != b.Facet {
			return a.Facet < b.Facet
		}
		if a.Value != b.Value {
			return a.Value < b.Value
		}
		return a.Term < b.Term
	})
	return out
}
