package npm

// IndexReads is how many cached packuments s has read for their versions.
func (s *Service) IndexReads() int64 { return s.indexReads.Load() }

// Renders is how many packument documents s has rendered.
func (s *Service) Renders() int64 { return s.renders.Load() }
