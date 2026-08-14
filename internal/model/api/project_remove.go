package api

type ProjectRemoveRequest struct {
	Name string
}

func (r ProjectRemoveRequest) Validate() error {
	if r.Name == "" {
		return ErrProjectNameRequired
	}
	return nil
}
