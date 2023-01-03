#!/usr/bin/env bash
# Run vela def gen-api to generate the API types from ./vela-templates/definitions/internal to ../vela-go-sdk

set -uo pipefail
go build -o ./bin/vela github.com/oam-dev/kubevela/references/cmd/cli

# for type in component/trait/policy/workflowstep
for type in component trait policy
do
    # for loop the file in ./vela-templates/definitions/internal/$type
    for file in ./vela-templates/definitions/internal/$type/*.cue
    do
        # get the file name
        filename=$(basename $file)
        # get the file name without suffix
        filename=${filename%.*}
        # generate the API types
        ./bin/vela def gen-api ./vela-templates/definitions/internal/$type/"$filename".cue --package-name "$filename" --output ../vela-go-sdk/$type/"$filename"/"$filename".go
    done
done