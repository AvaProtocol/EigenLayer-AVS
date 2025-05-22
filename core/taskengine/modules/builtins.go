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
		"moment": "libs/moment.min.js",
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
	
	moduleScript := `
	(function(module, exports) {
		%s
		
		return module.exports || exports;
	})({ exports: {} }, {});
	`
	
	script := fmt.Sprintf(moduleScript, string(content))
	
	result, err := runtime.RunString(script)
	if err != nil {
		return nil, err
	}
	
	return result, nil
}
