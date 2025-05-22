#!/bin/bash

LIBS_DIR="libs"
mkdir -p $LIBS_DIR

curl -s https://cdn.jsdelivr.net/npm/lodash@4.17.21/lodash.min.js -o $LIBS_DIR/lodash.min.js
echo "Downloaded lodash.min.js"

curl -s https://cdn.jsdelivr.net/npm/moment@2.29.4/min/moment.min.js -o $LIBS_DIR/moment.min.js
echo "Downloaded moment.min.js"

curl -s https://cdn.jsdelivr.net/npm/uuid@9.0.0/dist/umd/uuid.min.js -o $LIBS_DIR/uuid.min.js
echo "Downloaded uuid.min.js"

echo "All libraries downloaded successfully!"
