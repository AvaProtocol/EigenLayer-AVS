package modules

import (
	"errors"
	"sync"

	"github.com/dop251/goja"
)

type ModuleLoader interface {
	Load(runtime *goja.Runtime, name string) (goja.Value, error)
}

type Registry struct {
	loaders map[string]ModuleLoader
	cache   map[string]goja.Value
	mu      sync.RWMutex
}

func NewRegistry() *Registry {
	return &Registry{
		loaders: make(map[string]ModuleLoader),
		cache:   make(map[string]goja.Value),
	}
}

func (r *Registry) RegisterLoader(name string, loader ModuleLoader) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loaders[name] = loader
}

func (r *Registry) Require(runtime *goja.Runtime, name string) (goja.Value, error) {
	r.mu.RLock()
	cached, ok := r.cache[name]
	r.mu.RUnlock()
	if ok {
		return cached, nil
	}

	r.mu.RLock()
	loader, ok := r.loaders[name]
	r.mu.RUnlock()
	if !ok {
		return nil, errors.New("module not found: " + name)
	}

	exports, err := loader.Load(runtime, name)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.cache[name] = exports
	r.mu.Unlock()

	return exports, nil
}

func (r *Registry) RequireFunction(runtime *goja.Runtime) func(call goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(runtime.NewTypeError("require: module name must be provided"))
		}

		moduleName := call.Arguments[0].String()
		exports, err := r.Require(runtime, moduleName)
		if err != nil {
			panic(runtime.NewTypeError("require: " + err.Error()))
		}

		return exports
	}
}
