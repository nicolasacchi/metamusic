package yue2

// stopWords holds roughly 30 frequent function words per language (ISO 639-1), chosen to overlap as
// little as possible between languages. DetectLanguage scores lowercase word tokens against them.
var stopWords = map[string][]string{
	"it": {"il", "lo", "gli", "una", "che", "non", "per", "con", "sono", "ma", "come", "io", "noi", "voi",
		"nel", "della", "delle", "dei", "mio", "tuo", "anche", "questo", "quella", "sei", "siamo", "ancora",
		"sempre", "dove", "perché", "nella", "tutto"},
	"en": {"the", "and", "you", "that", "was", "for", "are", "with", "his", "they", "this", "have", "from",
		"not", "but", "what", "all", "were", "when", "your", "can", "said", "there", "each", "which", "she",
		"how", "will", "one", "our"},
	"es": {"el", "los", "las", "por", "para", "pero", "yo", "nosotros", "del", "mi", "está", "soy", "eres",
		"muy", "también", "esto", "esta", "cuando", "donde", "porque", "todo", "siempre", "sin", "tengo",
		"estoy", "vida", "corazón", "contigo", "hay", "usted", "nada"},
	"fr": {"le", "les", "des", "une", "est", "qui", "pas", "pour", "avec", "dans", "je", "nous", "vous", "ils",
		"mon", "ton", "sur", "mais", "comme", "suis", "très", "aussi", "cette", "tout", "où", "jamais",
		"toujours", "mes", "elle", "être"},
	"de": {"der", "die", "das", "und", "ist", "nicht", "ich", "du", "wir", "ihr", "sie", "mit", "von", "zu",
		"ein", "eine", "den", "dem", "auf", "für", "auch", "wie", "aber", "noch", "nur", "mein", "dein", "im",
		"bin", "immer"},
	"pt": {"os", "uma", "não", "você", "eu", "nós", "meu", "seu", "são", "sou", "muito", "isso", "esse",
		"onde", "tudo", "sempre", "ela", "ele", "dos", "das", "nos", "do", "da", "num", "mim", "estou", "tenho",
		"coração", "também", "então"},
}
