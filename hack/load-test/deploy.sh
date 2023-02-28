#!/bin/bash

BEGIN=${BEGIN:-1}
SIZE=${SIZE:-1000}
WORKER=${WORKER:-6}
VERSION=${VERSION:-1}
GROUP=${GROUP:-1}

SHARD=${SHARD:-3}

END=$(expr $BEGIN + $SIZE - 1)

run() {
  for i in $(seq $1 $3 $2); do
    sid=$((i % SHARD))
    gid=$((i % GROUP))
    v=${VERSION}
    cat ./app-templates/light_multi_cluster.yaml | sed 's/ID/'$i'/g' | sed 's/SHARD/'$sid'/g' | sed 's/VERSION/'$v'/g' | sed 's/GROUP/'$gid'/g'| kubectl apply -f -
    echo "worker $4: apply app $i to cluster:$cid shard:$sid"
  done
  echo "worker $4: done"
}

kubectl create ns load-test

for i in $(seq 1 $WORKER); do
  run $(expr $BEGIN + $i - 1) $END $WORKER $i &
done

wait