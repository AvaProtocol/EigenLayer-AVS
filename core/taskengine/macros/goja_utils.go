package macros

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"

	"github.com/dop251/goja"
)

// ConfigureGojaRuntime configures a new Goja JavaScript runtime instance.
// Its primary goals are:
//  1. To provide polyfills for common Web APIs (like crypto.getRandomValues) that JavaScript libraries
//     might expect, but which are not natively part of the Goja engine.
//  2. To standardize certain JavaScript behaviors (like Date object stringification and parsing of
//     date strings without explicit timezones when TZ=UTC) for consistency, especially in tests.
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
	// Why: Standardize JavaScript Date behavior for consistency, especially in tests.
	// JavaScript dates are notoriously tricky due to timezone interpretations.
	// These overrides aim to:
	// 1. Make `new Date("YYYY-MM-DDTHH:MM:SS")` (a date string without timezone) be interpreted
	//    as UTC if the Go environment has TZ=UTC. Goja doesn't do this robustly by default.
	// 2. Ensure `Date.prototype.toString()` always produces a predictable UTC-based string.

	// 1. Determine if Go's TZ is UTC and expose this to the JavaScript environment.
	//    The `__GO_PROCESS_TZ_IS_UTC__` global variable will be used by the Date constructor override.
	goTzIsUTC := os.Getenv("TZ") == "UTC"
	if err := runtime.Set("__GO_PROCESS_TZ_IS_UTC__", goTzIsUTC); err != nil {
		return // Failed to set helper global
	}

	// 2. Override Date constructor to interpret YYYY-MM-DDTHH:MM:SS as UTC if TZ=UTC in Go.
	//    Also, ensure Date.prototype.toString produces a consistent UTC string.
	//    How: A new JavaScript function `AvaDate` wraps the original `Date` constructor.
	//    - If `AvaDate` is called with `new` and a single string argument matching `YYYY-MM-DDTHH:MM:SS[.sss]`,
	//      AND `__GO_PROCESS_TZ_IS_UTC__` is true, it appends 'Z' to the string before passing it to the
	//      original `Date` constructor. This forces the date to be parsed as UTC.
	//    - In all other cases, `AvaDate` delegates to the original `Date` behavior.
	//    - `OriginalDate.prototype.toString` (and by extension `AvaDate.prototype.toString`) is overridden
	//      to call `this.toUTCString().replace('GMT', 'UTC')`, ensuring a consistent UTC string output
	//      (e.g., "Tue, 24 Oct 2023 12:34:56 UTC").
	_, err := runtime.RunString(`
		(function() {
			const OriginalDate = Date;
			const isoShortFormatRegex = /^\\\\d{4}-\\\\d{2}-\\\\d{2}T\\\\d{2}:\\\\d{2}:\\\\d{2}(\\\\.\\\\d{1,3})?$/;

			function AvaDate(...args) {
				if (
					args.length === 1 &&
					typeof args[0] === 'string' &&
					isoShortFormatRegex.test(args[0]) &&
					__GO_PROCESS_TZ_IS_UTC__
				) {
					// If it's a single string argument, in YYYY-MM-DDTHH:MM:SS[.sss] format,  // Corrected escaping for it's
					// and Go's TZ is UTC, interpret as UTC by appending 'Z'.
					return new OriginalDate(args[0] + 'Z');
				}
				// Otherwise, delegate to the original Date constructor
				if (new.target) { // Called with new
					return new OriginalDate(...args);
				}
				// Called as a function
				return OriginalDate(...args);
			}

			AvaDate.prototype = OriginalDate.prototype;
			AvaDate.now = OriginalDate.now;
			AvaDate.parse = OriginalDate.parse;
			AvaDate.UTC = OriginalDate.UTC;

			// Ensure toString always produces a consistent UTC representation for testing and internal consistency
			// This was the original override. Keeping it as it's beneficial for string formatting.
			OriginalDate.prototype.toString = function() {
				return this.toUTCString().replace('GMT', 'UTC');
			};
			
			// Also apply to our wrapper, though it should inherit via prototype.
			// However, direct assignment is safer if prototype chain is ever modified unexpectedly.
			AvaDate.prototype.toString = OriginalDate.prototype.toString;


			// Replace the global Date object
			Date = AvaDate;
		})();
	`)
	if err != nil {
		// If the Date override fails, log or handle as per application policy.
		// For now, just return. This might affect date parsing consistency.
		return
	}
}
