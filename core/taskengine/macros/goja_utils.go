package macros

import (
	"crypto/rand"
	"fmt"
	"strconv"

	"github.com/dop251/goja"
)

// ConfigureGojaRuntime configures a new Goja JavaScript runtime instance.
// Its primary goals are:
//  1. To provide polyfills for common Web APIs (like crypto.getRandomValues) that JavaScript libraries
//     might expect, but which are not natively part of the Goja engine.
//  2. To standardize certain JavaScript behaviors (like Date object stringification) for consistency
//     across environments. Date parsing follows Go runtime timezone settings for predictable behavior.
func ConfigureGojaRuntime(runtime *goja.Runtime) {
	// Override Object.prototype.toString to always return "[object Object]" for plain objects.
	// This mimics common browser behavior and can be important for how some JavaScript libraries
	// or logging mechanisms expect objects to be stringified.
	objectPrototype := runtime.Get("Object").ToObject(runtime).Get("prototype").ToObject(runtime)

	if err := objectPrototype.Set("toString", func() string {
		return "[object Object]"
	}); err != nil {
		// It's not critical if this fails, log it or handle as per application policy
		// For now, we just return to avoid further issues if the runtime is unstable.
		return
	}

	// Add crypto polyfill for UUID library support and other cryptographic needs in JavaScript.
	// Why: `crypto.getRandomValues` is a Web Cryptography API used to generate cryptographically
	//      strong random numbers. It's not available in a bare Goja environment.
	// How: This polyfill implements `crypto.getRandomValues` using Go's `crypto/rand` package
	//      to generate secure random bytes. It expects a typed array (e.g., Uint8Array) as an
	//      argument from JavaScript, fills it with these random bytes, and returns it.
	crypto := runtime.NewObject()
	if err := crypto.Set("getRandomValues", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) < 1 {
			panic(runtime.NewTypeError("crypto.getRandomValues: array argument required"))
		}

		// Get the array argument
		arrayArg := call.Arguments[0]
		if arrayArg == nil || goja.IsUndefined(arrayArg) || goja.IsNull(arrayArg) {
			panic(runtime.NewTypeError("crypto.getRandomValues: array argument required"))
		}

		arrayObj := arrayArg.ToObject(runtime)
		if arrayObj == nil {
			panic(runtime.NewTypeError("crypto.getRandomValues: invalid array argument, not an object"))
		}

		// Check for ArrayBufferView types which are common inputs (e.g., Uint8Array)
		// byteLengthProp := arrayObj.Get("byteLength") // This is a good indicator

		lengthVal := arrayObj.Get("length")
		// If 'length' is not present, it might not be a typical array-like structure expected by getRandomValues
		if lengthVal == nil || goja.IsUndefined(lengthVal) || goja.IsNull(lengthVal) {
			panic(runtime.NewTypeError("crypto.getRandomValues: array argument must have a length property"))
		}

		length := int(lengthVal.ToInteger())
		// Limit length to a reasonable maximum as in browser implementations
		if length < 0 || length > 65536 { // Changed from <=0 to <0, length 0 is valid but does nothing.
			panic(runtime.NewTypeError("crypto.getRandomValues: array length must be between 0 and 65536"))
		}
		if length == 0 {
			return arrayArg // No-op for 0 length
		}

		// Generate random bytes
		randomBytes := make([]byte, length)
		if _, err := rand.Read(randomBytes); err != nil {
			// It's better to throw a Goja error that JS can catch if possible
			panic(runtime.NewGoError(err))
		}

		// Fill the array with random values
		// This assumes the array elements are assignable as numbers.
		// For typed arrays (like Uint8Array), this direct setting might be problematic
		// if goja doesn't handle the conversion correctly.
		// A more robust approach might involve checking array type (e.g. instanceof Uint8Array)
		// and using appropriate methods if available, or creating a new typed array.
		for i := 0; i < length; i++ {
			// Goja should handle the conversion from int to the appropriate JS number type.
			if err := arrayObj.Set(strconv.Itoa(i), int64(randomBytes[i])); err != nil {
				panic(runtime.NewTypeError(fmt.Sprintf("crypto.getRandomValues: failed to set array value at index %d", i)))
			}
		}

		return arrayArg
	}); err != nil {
		return // crypto.Set failed
	}

	if err := runtime.Set("crypto", crypto); err != nil {
		return // runtime.Set failed
	}

	// --- Date Handling Overrides ---
	// Why: Standardize JavaScript Date behavior for consistency across all environments.
	// JavaScript dates are notoriously tricky due to timezone interpretations.
	// This override ensures `Date.prototype.toString()` always produces a predictable UTC-based string.

	// Override Date.prototype.toString to always produce a consistent UTC representation
	// This ensures consistent string formatting for dates across different environments
	_, err := runtime.RunString(`
		(function() {
			const OriginalDate = Date;

			// Ensure toString always produces a consistent UTC representation for testing and internal consistency
			OriginalDate.prototype.toString = function() {
				return this.toUTCString().replace('GMT', 'UTC');
			};
		})();
	`)
	if err != nil {
		// If the Date override fails, log or handle as per application policy.
		// For now, just return. This might affect date parsing consistency.
		return
	}
}
