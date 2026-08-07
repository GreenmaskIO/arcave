package arcaved

type Service struct {
}

func (s *Service) ListSandboxProviders() []any {
	panic("implement me")
}

func (s *Service) ListStackProviders() []any {
	panic("implement me")
}

func (s *Service) ListProjects() []any {
	panic("implement me")
}

func (s *Service) ListSessions() []any {
	panic("implement me")
}

func (s *Service) ListSandboxSavepoints() []any {

}

func (s *Service) ListStackSavepoints() []any {

}

func (s *Service) Freeze() []any {

}

func (s *Service) ListTimelines() []any {

}

func (s *Service) CurrentTimeline() []any {

}

func (s *Service) RunGarbageCollector() []any {

}
