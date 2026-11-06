package app

import "github.com/nicolasacchi/metamusic/internal/attention"

// buildM5Attention constructs the attention service and wires the close-the-loop block of /context (lane M5-A1 owns this file).
func buildM5Attention(in m5Inputs, s *m5Services) {
	s.Attention = attention.New(attention.Options{Store: in.st})
	if in.ctxb != nil {
		in.ctxb.UseCloseLoop(s.Attention)
	}
}
