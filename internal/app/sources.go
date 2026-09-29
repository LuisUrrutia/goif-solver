package app

import (
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

func (r *Runtime) sources(c config.Config) ([]intent.Source, error) {
	sources := make([]intent.Source, 0, len(c.Sources))
	for _, definition := range c.Sources {
		var factory func(config.Source) (intent.Source, error)
		if definition.Provider != "" {
			factory = r.Providers[definition.Provider].stream
		} else if execution := r.Executions[definition.Protocol]; execution != nil {
			factory = execution.Source
		}
		if factory == nil {
			return nil, errors.New("selected adapter has no discovery source")
		}
		source, err := factory(definition)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}
