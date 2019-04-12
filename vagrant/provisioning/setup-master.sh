#!/bin/bash

# Save trace setting
XTRACE=$(set +o | grep xtrace)
set -o xtrace

MASTER1=$1
MASTER2=$2
MASTER3=$3
NODE_NAME=$4
PUBLIC_SUBNET_MASK=$5
GW_IP=$6
OVN_EXTERNAL=$7

if [ -n "$OVN_EXTERNAL" ]; then
    MASTER1=`ifconfig enp0s8 | grep 'inet addr' | cut -d: -f2 | awk '{print $1}'`
    PUBLIC_SUBNET_MASK=`ifconfig enp0s8 | grep 'inet addr' | cut -d: -f4`
    GW_IP=`grep 'option routers' /var/lib/dhcp/dhclient.enp0s8.leases | head -1 | sed -e 's/;//' | awk '{print $3}'`
fi

OVERLAY_IP=$MASTER1

cat > setup_master_args.sh <<EOL
OVERLAY_IP=$OVERLAY_IP
MASTER1=$MASTER1
MASTER2=$MASTER2
MASTER3=$MASTER3
PUBLIC_SUBNET_MASK=$PUBLIC_SUBNET_MASK
GW_IP=$GW_IP
NODE_NAME=$NODE_NAME
OVN_EXTERNAL=$OVN_EXTERNAL
EOL

# Comment out the next line if you don't prefer daemonsets.
DAEMONSET="true"

# Comment out the next line, if you prefer TCP instead of SSL.
SSL="true"

# Set HA to "true" if you want OVN HA
HA="false"

# FIXME(mestery): Remove once Vagrant boxes allow apt-get to work again
sudo rm -rf /var/lib/apt/lists/*

# Install CNI
pushd ~/
wget -nv https://github.com/containernetworking/cni/releases/download/v0.5.2/cni-amd64-v0.5.2.tgz
popd
sudo mkdir -p /opt/cni/bin
pushd /opt/cni/bin
sudo tar xvzf ~/cni-amd64-v0.5.2.tgz
popd
sudo mkdir -p /etc/cni/net.d
# Create a 99loopback.conf to have atleast one CNI config.
echo '{
    "cniVersion": "0.2.0",
    "type": "loopback"
}' | sudo tee /etc/cni/net.d/99loopback.conf

# Add external repos to install docker, k8s and OVS from packages.
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates
echo "deb https://apt.kubernetes.io/ kubernetes-xenial main" |  sudo tee /etc/apt/sources.list.d/kubernetes.list
curl -s https://packages.cloud.google.com/apt/doc/apt-key.gpg | sudo apt-key add -
echo "deb http://18.191.116.101/openvswitch/stable /" |  sudo tee /etc/apt/sources.list.d/openvswitch.list
wget -O - http://18.191.116.101/openvswitch/keyFile |  sudo apt-key add -
sudo apt-key adv --keyserver hkp://keyserver.ubuntu.com:80 --recv-keys 58118E89F3A912897C070ADBF76221572C52609D
sudo su -c "echo \"deb https://apt.dockerproject.org/repo ubuntu-xenial main\" >> /etc/apt/sources.list.d/docker.list"
sudo apt-get update

## First, install docker
sudo apt-get purge lxc-docker
sudo apt-get install -y linux-image-extra-$(uname -r) linux-image-extra-virtual
sudo apt-get install -y docker-engine
sudo service docker start

## Install kubernetes
sudo apt-get install -y kubelet kubeadm kubectl
sudo apt-mark hold kubelet kubeadm kubectl
sudo service kubelet restart

sudo swapoff -a
sudo kubeadm config images pull
sudo kubeadm init --pod-network-cidr=192.168.0.0/16 --apiserver-advertise-address=$OVERLAY_IP \
	--service-cidr=172.16.1.0/24 2>&1 | tee kubeadm.log
grep -A1 "kubeadm join" kubeadm.log | sudo tee /vagrant/kubeadm.log

mkdir -p $HOME/.kube
sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config
sudo chown $(id -u):$(id -g) $HOME/.kube/config

# Wait till kube-apiserver is up
while true; do
    kubectl get node $NODE_NAME
    if [ $? -eq 0 ]; then
        break
    fi
    echo "waiting for kube-apiserver to be up"
    sleep 1
done

# Let master run pods too.
kubectl taint nodes --all node-role.kubernetes.io/master-

## install packages that deliver ovs-pki and its dependencies
sudo apt-get build-dep dkms
sudo apt-get install python-six openssl python-pip -y
sudo apt-get install openvswitch-common libopenvswitch -y
sudo apt-get install openvswitch-datapath-dkms -y

if [ "$DAEMONSET" != "true" ]; then
  ## Install OVS and OVN components
  sudo apt-get install openvswitch-switch
  sudo apt-get install ovn-central ovn-common ovn-host -y
fi
if [ -n "$SSL" ]; then
    PROTOCOL=ssl
    echo "PROTOCOL=ssl" >> setup_master_args.sh
    # Install SSL certificates
    pushd /etc/openvswitch
    sudo ovs-pki -d /vagrant/pki init --force
    sudo ovs-pki req ovnsb && sudo ovs-pki self-sign ovnsb

    sudo ovs-pki req ovnnb && sudo ovs-pki self-sign ovnnb

    sudo ovs-pki req ovncontroller
    sudo ovs-pki -b -d /vagrant/pki sign ovncontroller switch
    popd
else
    PROTOCOL=tcp
    echo "PROTOCOL=tcp" >> setup_master_args.sh
fi

if [ "$HA" = "true" ]; then
    sudo /usr/share/openvswitch/scripts/ovn-ctl stop_nb_ovsdb
    sudo /usr/share/openvswitch/scripts/ovn-ctl stop_sb_ovsdb
    sudo rm /etc/openvswitch/ovn*.db
    sudo /usr/share/openvswitch/scripts/ovn-ctl stop_northd

    LOCAL_IP=$OVERLAY_IP

    sudo /usr/share/openvswitch/scripts/ovn-ctl \
        --db-nb-cluster-local-addr=$LOCAL_IP start_nb_ovsdb

    sudo /usr/share/openvswitch/scripts/ovn-ctl \
        --db-sb-cluster-local-addr=$LOCAL_IP start_sb_ovsdb
    
    ovn_nb="$PROTOCOL:$MASTER1:6641,$PROTOCOL:$MASTER2:6641,$PROTOCOL:$MASTER3:6641"
    ovn_sb="$PROTOCOL:$MASTER1:6642,$PROTOCOL:$MASTER2:6642,$PROTOCOL:$MASTER3:6642"

    sudo ovn-northd -vconsole:emer -vsyslog:err -vfile:info \
    --ovnnb-db="$ovn_nb" --ovnsb-db="$ovn_sb" --no-chdir \
    --log-file=/var/log/openvswitch/ovn-northd.log \
    --pidfile=/var/run/openvswitch/ovn-northd.pid --detach --monitor
fi


# Clone ovn-kubernetes repo
mkdir -p $HOME/work/src/github.com/openvswitch
pushd $HOME/work/src/github.com/openvswitch
git clone https://github.com/openvswitch/ovn-kubernetes
popd

if [ "$DAEMONSET" != "true" ]; then
  # Install golang
  wget -nv https://dl.google.com/go/go1.9.2.linux-amd64.tar.gz
  sudo tar -C /usr/local -xzf go1.9.2.linux-amd64.tar.gz
  export PATH="/usr/local/go/bin:echo $PATH"
  export GOPATH=$HOME/work

  pushd $HOME/work/src/github.com/openvswitch/ovn-kubernetes/go-controller
  make 1>&2 2>/dev/null
  sudo make install
  popd

  if [ $PROTOCOL = "ssl" ]; then
   sudo ovn-nbctl set-connection pssl:6641 -- set connection . inactivity_probe=0
   sudo ovn-sbctl set-connection pssl:6642 -- set connection . inactivity_probe=0
   sudo ovn-nbctl set-ssl /etc/openvswitch/ovnnb-privkey.pem \
    /etc/openvswitch/ovnnb-cert.pem /vagrant/pki/switchca/cacert.pem
   sudo ovn-sbctl set-ssl /etc/openvswitch/ovnsb-privkey.pem \
    /etc/openvswitch/ovnsb-cert.pem /vagrant/pki/switchca/cacert.pem
   SSL_ARGS="-nb-client-privkey /etc/openvswitch/ovncontroller-privkey.pem \
   -nb-client-cert /etc/openvswitch/ovncontroller-cert.pem \
   -nb-client-cacert /etc/openvswitch/ovnnb-cert.pem \
   -sb-client-privkey /etc/openvswitch/ovncontroller-privkey.pem \
   -sb-client-cert /etc/openvswitch/ovncontroller-cert.pem \
   -sb-client-cacert /etc/openvswitch/ovnsb-cert.pem"
  elif [ $PROTOCOL = "tcp" ]; then
   sudo ovn-nbctl set-connection ptcp:6641 -- set connection . inactivity_probe=0
   sudo ovn-sbctl set-connection ptcp:6642 -- set connection . inactivity_probe=0
  fi

  if [ "$HA" = "true" ]; then
      ovn_nb="$PROTOCOL://$MASTER1:6641,$PROTOCOL://$MASTER2:6641,$PROTOCOL://$MASTER3:6641"
      ovn_sb="$PROTOCOL://$MASTER1:6642,$PROTOCOL://$MASTER2:6642,$PROTOCOL://$MASTER3:6642"
  else
      ovn_nb="$PROTOCOL://$OVERLAY_IP:6641"
      ovn_sb="$PROTOCOL://$OVERLAY_IP:6642"
  fi

  sudo kubectl create -f /vagrant/ovnkube-rbac.yaml

  SECRET=`kubectl get secret | grep ovnkube | awk '{print $1}'`
  TOKEN=`kubectl get secret/$SECRET -o yaml |grep "token:" | cut -f2  -d ":" | sed 's/^  *//' | base64 -d`
  echo $TOKEN > /vagrant/token

  nohup sudo ovnkube -net-controller -loglevel=4 \
   -k8s-apiserver="https://$OVERLAY_IP:6443" \
   -k8s-cacert=/etc/kubernetes/pki/ca.crt \
   -k8s-token="$TOKEN" \
   -logfile="/var/log/ovn-kubernetes/ovnkube.log" \
   -init-master="k8smaster" -cluster-subnet="192.168.0.0/16" \
   -init-node="k8smaster" \
   -service-cluster-ip-range=172.16.1.0/24 \
   -nodeport \
   -nb-address="$ovn_nb" \
   -sb-address="$ovn_sb" \
   -init-gateways -gateway-local \
   ${SSL_ARGS} 2>&1 &
else
  # Daemonset is enabled.

  # Dameonsets only work with TCP now.
  PROTOCOL="tcp"

  # cleanup /etc/hosts as it incorrectly maps the hostname to `127.0.1.1`
  sudo sed -i '/^127.0.1.1/d' /etc/hosts

  # Make daemonset yamls
  pushd $HOME/work/src/github.com/openvswitch/ovn-kubernetes/dist/images
  make daemonsetyaml 1>&2 2>/dev/null
  popd

  # label the master node for daemonsets
  kubectl label node k8smaster node-role.kubernetes.io/master=true --overwrite

  # Create OVN namespace, service accounts, ovnkube-master headless service, and policies
  kubectl create -f $HOME/work/src/github.com/openvswitch/ovn-kubernetes/dist/yaml/ovn-setup.yaml

  # Delete ovn config map that was created by default in ovn-setup.yaml
  kubectl delete configmap ovn-config -n ovn-kubernetes

  # Create ovn config map.
  cat << EOF | kubectl create -f - > /dev/null 2>&1
kind: ConfigMap
apiVersion: v1
metadata:
  name: ovn-config
  namespace: ovn-kubernetes
data:
  k8s_apiserver: "https://$OVERLAY_IP:6443"
  net_cidr:      "192.168.0.0/16"
  svc_cidr:      "172.16.1.0/24"
EOF

  # Run ovnkube-db daemonset.
  kubectl create -f $HOME/work/src/github.com/openvswitch/ovn-kubernetes/dist/yaml/ovnkube-db.yaml

  # Run ovnkube-master daemonset.
  kubectl create -f $HOME/work/src/github.com/openvswitch/ovn-kubernetes/dist/yaml/ovnkube-master.yaml

  # Run ovnkube daemonsets for nodes
  kubectl create -f $HOME/work/src/github.com/openvswitch/ovn-kubernetes/dist/yaml/ovnkube-node.yaml
fi

# Setup some example yaml files
cat << APACHEPOD >> ~/apache-pod.yaml
apiVersion: v1
kind: Pod
metadata:
  name: apachetwin
  labels:
    name: webserver
spec:
  containers:
  - name: apachetwin
    image: fedora/apache
APACHEPOD

cat << NGINXPOD >> ~/nginx-pod.yaml
apiVersion: v1
kind: Pod
metadata:
  name: nginxtwin
  labels:
    name: webserver
spec:
  containers:
  - name: nginxtwin
    image: nginx
NGINXPOD

cat << APACHEEW >> ~/apache-e-w.yaml
apiVersion: v1
kind: Service
metadata:
  labels:
    name: apacheservice
    role: service
  name: apacheservice
spec:
  ports:
    - port: 8800
      targetPort: 80
      protocol: TCP
      name: tcp
  selector:
    name: webserver
APACHEEW

cat << APACHENS >> ~/apache-n-s.yaml
apiVersion: v1
kind: Service
metadata:
  labels:
    name: apacheexternal
    role: service
  name: apacheexternal
spec:
  ports:
    - port: 8800
      targetPort: 80
      protocol: TCP
      name: tcp
  selector:
    name: webserver
  type: NodePort
APACHENS

sleep 10

# Restore xtrace
$XTRACE
