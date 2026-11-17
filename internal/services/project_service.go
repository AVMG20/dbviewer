package services

import (
	"dbviewer/internal/model"
)

// ProjectService: projects, saved queries, UI state and app info.
type ProjectService struct{ c *Core }

func NewProjectService(c *Core) *ProjectService { return &ProjectService{c: c} }

// AppInfo returns version, data dir and platform.
func (s *ProjectService) AppInfo() model.AppInfo {
	return model.AppInfo{Version: Version, DataDir: s.c.Store.Dir(), Platform: platform()}
}

// Projects lists all projects ordered by sortOrder. There is always at least one
// ("Default" is created on first start).
func (s *ProjectService) Projects() ([]model.Project, error) {
	p, err := s.c.Store.Projects()
	return ret(p, err)
}

// SaveProject creates (id "") or renames/reorders a project.
func (s *ProjectService) SaveProject(p model.Project) (model.Project, error) {
	p, err := s.c.Store.SaveProject(p)
	return ret(p, err)
}

// DeleteProject deletes a project with all its connections (and their saved
// secrets, history) and saved queries. Refuses to delete the last project.
func (s *ProjectService) DeleteProject(id string) error {
	ids, err := s.c.Store.DeleteProject(id)
	for _, cid := range ids {
		s.c.Conns.Disconnect(cid)
		s.c.Meta.Invalidate(cid, "")
	}
	return wrap(err)
}

// ReorderProjects sets sortOrder to the index in ids.
func (s *ProjectService) ReorderProjects(ids []string) error {
	return wrap(s.c.Store.ReorderProjects(ids))
}

// SavedQueries lists saved queries of a project ("" = all), ordered by name.
func (s *ProjectService) SavedQueries(projectID string) ([]model.SavedQuery, error) {
	q, err := s.c.Store.SavedQueries(projectID)
	return ret(q, err)
}

// SaveQuery creates (id "") or updates a saved query. name and projectId required.
func (s *ProjectService) SaveQuery(q model.SavedQuery) (model.SavedQuery, error) {
	q, err := s.c.Store.SaveQuery(q)
	return ret(q, err)
}

func (s *ProjectService) DeleteSavedQuery(id string) error {
	return wrap(s.c.Store.DeleteSavedQuery(id))
}

// GetUIState returns an opaque frontend-owned value ("" if unset).
// Suggested keys: "tabs:<projectId>", "colwidths:<connId>:<schema>.<table>",
// "filters:<connId>:<schema>.<table>", "ui:prefs".
func (s *ProjectService) GetUIState(key string) (string, error) {
	v, err := s.c.Store.UIState(key)
	return ret(v, err)
}

// SetUIState stores an opaque value; "" deletes the key.
func (s *ProjectService) SetUIState(key, value string) error {
	return wrap(s.c.Store.SetUIState(key, value))
}
