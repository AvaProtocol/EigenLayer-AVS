#!/bin/bash

# Fix all TriggerReason structures in engine_test.go
cd /Users/mikasa/Code/EigenLayer-AVS

# Replace the Data field pattern with the correct structure
perl -i -pe 's/Data: &avsproto\.TriggerReason_Block\{\s*Block: &avsproto\.BlockTriggerData\{\s*BlockNumber: uint64\((\d+)\),\s*\},\s*\},/BlockNumber: uint64($1),\n\t\t\tType:        avsproto.TriggerReason_Block,/g' core/taskengine/engine_test.go

echo "Fixed TriggerReason structures in engine_test.go" 