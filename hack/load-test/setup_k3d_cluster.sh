SIZE=${SIZE:-1}
BEGIN=${BEGIN:-1}
IP=$(curl https://api.ipify.org/)
END=$(expr $BEGIN + $SIZE - 1)
GROUP=${GROUP:-1}


run() {
    i=$1
    port=$(expr $i + 6444)
    cat ./config.yaml | sed 's/ID/'$i'/g' | sed 's/HOST/'$IP'/g' | sed 's/PORT/'$port'/g' > ./config-$i.yaml
    k3d cluster delete cluster-$i
    k3d cluster create --servers-memory 2G --config ./config-$i.yaml
    kconfig=$(k3d kubeconfig write cluster-$i)
    kubectl create ns load-test --kubebconfig $kconfig
    echo "cluster-$i created with ./config-$i.yaml at $kconfig"
    vela cluster detach cluster-$i || true
    vela cluster join $kconfig --name cluster-$i
    echo "cluster-$i joined"
    vela cluster labels add cluster-$1 load-test/group=$group
}

for i in $(seq $BEGIN $END); do
    run $i
done
