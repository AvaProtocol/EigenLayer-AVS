package macros

import (
	"github.com/dop251/goja"
)

func ConfigureGojaRuntime(runtime *goja.Runtime) {
	objectPrototype := runtime.Get("Object").ToObject(runtime).Get("prototype").ToObject(runtime)

	objectPrototype.Set("toString", func() string {
		return "[object Object]"
	})
}
