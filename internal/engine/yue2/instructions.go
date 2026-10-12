package yue2

// Instructions is copied verbatim from yue2-infer 0.1.6 src/yue2/protocol.py INSTRUCTIONS
// (PROTOCOL_VERSION "yue2-native-v1"); golden-tested against testdata/instructions.json.
var Instructions = map[string]string{
	"off":    "Generate music with codec tokens from the given conditions.",
	"melody": "Generate a melody-only ABC transcription without chord symbols, then generate music with codec tokens from the given conditions.",
	"full":   "Generate a chord-annotated ABC transcription, then generate music with codec tokens from the given conditions.",
}
