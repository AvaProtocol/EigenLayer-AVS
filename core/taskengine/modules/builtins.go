package modules

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/dop251/goja"
)

//go:embed libs/*
var builtinLibs embed.FS

type BuiltinLoader struct {
	libraries map[string][]byte
}

func NewBuiltinLoader() *BuiltinLoader {
	return &BuiltinLoader{
		libraries: make(map[string][]byte),
	}
}

func (l *BuiltinLoader) RegisterBuiltinLibraries() error {
	libMap := map[string]string{
		"lodash": "libs/lodash.min.js",
		"dayjs":  "libs/dayjs.min.js",
		"uuid":   "libs/uuid.min.js",
	}

	for name, path := range libMap {
		content, err := fs.ReadFile(builtinLibs, path)
		if err != nil {
			return err
		}
		l.libraries[name] = content
	}

	return nil
}

func (l *BuiltinLoader) Load(runtime *goja.Runtime, name string) (goja.Value, error) {
	content, ok := l.libraries[name]
	if !ok {
		return nil, errors.New("module not found: " + name)
	}

	// Create module and exports objects
	module := runtime.NewObject()
	exports := runtime.NewObject()
	module.Set("exports", exports)

	// Set the module and exports objects in the runtime
	runtime.Set("module", module)
	runtime.Set("exports", exports)

	// Special handling for Day.js - it's designed to work well with module systems
	if name == "dayjs" {
		// Day.js has better UMD support, so we can run it more simply
		moduleScript := fmt.Sprintf(`
			(function(module, exports) {
				%s
				return module.exports;
			})(module, exports);
		`, string(content))

		result, err := runtime.RunString(moduleScript)
		if err != nil {
			return nil, fmt.Errorf("error loading module %s: %v", name, err)
		}
		return result, nil
	}

	// Wrap the code so that 'this' is the global object
	moduleScript := fmt.Sprintf(`
		(function(module, exports) {
			(function() {
				%s
			}).call(this);
			return module.exports || exports;
		})(module, exports);
	`, string(content))

	result, err := runtime.RunString(moduleScript)
	if err != nil {
		return nil, fmt.Errorf("error loading module %s: %v", name, err)
	}

	return result, nil
}
