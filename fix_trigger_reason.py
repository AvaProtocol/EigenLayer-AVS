#!/usr/bin/env python3

import re

# Read the file
with open('core/taskengine/engine_test.go', 'r') as f:
    content = f.read()

# Pattern to match the old TriggerReason structure
old_pattern = r'Reason: &avsproto\.TriggerReason\{\s*Data: &avsproto\.TriggerReason_Block\{\s*Block: &avsproto\.BlockTriggerData\{\s*BlockNumber: uint64\((\d+)\),\s*\},\s*\},\s*\},'

# Replacement pattern
new_pattern = r'Reason: &avsproto.TriggerReason{\n\t\t\tBlockNumber: uint64(\1),\n\t\t\tType:        avsproto.TriggerReason_Block,\n\t\t},'

# Replace all occurrences
content = re.sub(old_pattern, new_pattern, content, flags=re.MULTILINE | re.DOTALL)

# Also fix the test assertions that use GetBlock()
content = re.sub(r'execution\.Reason\.GetBlock\(\)\.BlockNumber', 'execution.Reason.BlockNumber', content)
content = re.sub(r'execution\.Reason\.GetBlock\(\) == nil \|\|', '', content)
content = re.sub(r'execution\.Reason != nil && execution\.Reason\.GetBlock\(\) != nil', 'execution.Reason != nil', content)

# Write the file back
with open('core/taskengine/engine_test.go', 'w') as f:
    f.write(content)

print("Fixed all TriggerReason structures in engine_test.go") 