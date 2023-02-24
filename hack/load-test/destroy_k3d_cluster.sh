IZE=${SIZE:-1}
IP=$(curl https://api.ipify.org/)

run() {
    i=$1
    port=$(expr $i + 6440)
    cat ./config.yaml | sed 's/ID/'$i'/g' | sed 's/HOST/'$IP'/g' | sed 's/PORT/'$port'/g' > ./config-$i.yaml
    k3d cluster delete cluster-$i
    k3d cluster create --servers-memory 2G --config ./config-$i.yaml
    kconfig=$(k3d kubeconfig write cluster-$i)
    echo "cluster-$i created with ./config-$i.yaml at $kconfig"
    vela cluster detach cluster-$i || true
    vela cluster join $kconfig --name cluster-$i
    echo "cluster-$i joined"
}

for i in $(seq 1 $SIZE); do
    run $i
done
~