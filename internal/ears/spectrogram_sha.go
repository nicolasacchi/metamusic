package ears

import "context"

// SpectrogramSHA256 returns the sha256 of the track's stored spectrogram rendition, or "" when it has
// none. /context links the picture through it (C51.8) without reading the file.
func (s *Service) SpectrogramSHA256(ctx context.Context, trackID string) (string, error) {
	if err := s.wired(); err != nil {
		return "", err
	}
	_, sp, err := s.loadRows(ctx, trackID)
	if err != nil || sp == nil {
		return "", err
	}
	return sp.SHA256, nil
}
