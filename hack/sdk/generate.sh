#!/usr/bin/env bash
# Run vela def gen-api to generate the API types from ./vela-templates/definitions/internal to ../vela-go-sdk

set -uo pipefail
go build -o ./bin/vela github.com/oam-dev/kubevela/references/cmd/cli

# for loop the file in ./vela-templates/definitions/internal/component
for file in ./vela-templates/definitions/internal/component/*.cue
do
    # get the file name
    filename=$(basename $file)
    # get the file name without suffix
    filename=${filename%.*}
    # generate the API types
     ./bin/vela def gen-api ./vela-templates/definitions/internal/component/"$filename".cue --package-name "$filename" --output ../vela-go-sdk/component/"$filename"/"$filename".go

done

for file in ./vela-templates/definitions/internal/trait/*.cue
do
    # get the file name
    filename=$(basename $file)
    # get the file name without suffix
    filename=${filename%.*}
    # generate the API types
    ./bin/vela def gen-api ./vela-templates/definitions/internal/trait/"$filename".cue --package-name "$filename" --output ../vela-go-sdk/trait/"$filename"/"$filename".go
done
