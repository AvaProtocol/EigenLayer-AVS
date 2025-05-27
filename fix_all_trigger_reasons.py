#!/usr/bin/env python3

import re

# Read the file
with open('core/taskengine/engine_test.go', 'r') as f:
    content = f.read()

# Pattern to match TriggerReason structures that have BlockNumber but no Type field
# This pattern looks for structures that have BlockNumber but don't already have Type
pattern = r'(&avsproto\.TriggerReason\{\s*BlockNumber: uint64\([^}]+\),\s*\})'

def replacement_func(match):
    # Extract the matched TriggerReason structure
    original = match.group(1)
    
    # Check if it already has Type field
    if 'Type:' in original:
        return original  # Already has Type field, don't modify
    
    # Add Type field before the closing brace
    modified = original.replace('},', ',\n\t\t\tType:        avsproto.TriggerReason_Block,\n\t\t},')
    return modified

# Replace all occurrences
content = re.sub(pattern, replacement_func, content, flags=re.MULTILINE | re.DOTALL)

# Write the file back
with open('core/taskengine/engine_test.go', 'w') as f:
    f.write(content)

print("Fixed all TriggerReason structures in engine_test.go") 