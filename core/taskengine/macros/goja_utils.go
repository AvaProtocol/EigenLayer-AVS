package macros

import (
	"crypto/rand"

	"github.com/dop251/goja"
)

func ConfigureGojaRuntime(runtime *goja.Runtime) {
	objectPrototype := runtime.Get("Object").ToObject(runtime).Get("prototype").ToObject(runtime)

	if err := objectPrototype.Set("toString", func() string {
		return "[object Object]"
	}); err != nil {
		return
	}

	// Add crypto polyfill for UUID library support
	crypto := runtime.NewObject()
	if err := crypto.Set("getRandomValues", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(runtime.NewTypeError("crypto.getRandomValues: array argument required"))
		}

		// Get the array argument
		arrayArg := call.Arguments[0]
		if arrayArg == nil {
			panic(runtime.NewTypeError("crypto.getRandomValues: array argument required"))
		}

		// Convert to object to access array properties
		arrayObj := arrayArg.ToObject(runtime)
		if arrayObj == nil {
			panic(runtime.NewTypeError("crypto.getRandomValues: invalid array argument"))
		}

		// Get array length
		lengthVal := arrayObj.Get("length")
		if lengthVal == nil {
			panic(runtime.NewTypeError("crypto.getRandomValues: array argument required"))
		}

		length := int(lengthVal.ToInteger())
		if length <= 0 || length > 65536 {
			panic(runtime.NewTypeError("crypto.getRandomValues: array length must be between 1 and 65536"))
		}

		// Generate random bytes
		randomBytes := make([]byte, length)
		if _, err := rand.Read(randomBytes); err != nil {
			panic(runtime.NewTypeError("crypto.getRandomValues: failed to generate random values"))
		}

		// Fill the array with random values
		for i := 0; i < length; i++ {
			if err := arrayObj.Set(string(rune('0'+i)), int(randomBytes[i])); err != nil {
				panic(runtime.NewTypeError("crypto.getRandomValues: failed to set array value"))
			}
		}

		return arrayArg
	}); err != nil {
		return
	}

	if err := runtime.Set("crypto", crypto); err != nil {
		return
	}

	// Override Date toString method to always show UTC for consistent test results
	// This is more targeted than overriding the constructor and won't interfere with libraries like Day.js
	_, err := runtime.RunString(`
		(function() {
			// Only override toString to show UTC format for test consistency
			Date.prototype.toString = function() {
				return this.toUTCString().replace('GMT', 'UTC');
			};
		})();
	`)
	if err != nil {
		// If the Date override fails, just continue - it's not critical
		return
	}
}
