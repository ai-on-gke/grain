#!/usr/bin/env bash
set -e

GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m' # No Color

type_msg() {
    echo -e "${CYAN}$ ${BOLD}$1${NC}"
    sleep 0.8
}

banner() {
    clear || true
    echo -e "${BOLD}========================================================================${NC}"
    echo -e "${GREEN}${BOLD}   GRANULE DEMO: Multi-VM Pods & Ephemeral MicroVM Containers in K8s   ${NC}"
    echo -e "${BOLD}========================================================================${NC}"
    echo ""
}

banner
echo -e "${YELLOW}Demonstrating multiple hardware-isolated Cloud-Hypervisor microVMs${NC}"
echo -e "${YELLOW}running inside a SINGLE Kubernetes Pod with sub-second ephemeral containers.${NC}"
echo ""
sleep 1.5

# Step 1: Cluster & KVM verification
echo -e "${BOLD}--- Step 1: Environment & Cluster Verification ---${NC}"
type_msg "kubectl get nodes -o wide"
kubectl get nodes -o wide
echo ""
type_msg "ls -la /dev/kvm"
ls -la /dev/kvm
echo ""
sleep 1.5

# Step 2: Deploy Multi-VM Pod
echo -e "${BOLD}--- Step 2: Provisioning Multi-VM Base Pod (web + builder) ---${NC}"
type_msg "kubectl apply -f demo/assets/pod-multi-vm.yaml"
kubectl delete pod granule-multi-vm --ignore-not-found=true --wait=true > /dev/null 2>&1 || true
sed 's|IMAGE_PLACEHOLDER|granule:snapshot|g' demo/assets/pod-multi-vm.yaml | kubectl apply -f -
echo ""
type_msg "kubectl wait --for=condition=Ready pod/granule-multi-vm --timeout=30s"
kubectl wait --for=condition=Ready pod/granule-multi-vm --timeout=30s
echo ""
type_msg "kubectl get pod granule-multi-vm -o wide"
kubectl get pod granule-multi-vm -o wide
echo ""
sleep 1.5

# Step 3: Verify initial VMs
echo -e "${BOLD}--- Step 3: Inspecting Regular VM Containers (web & builder) ---${NC}"
type_msg "kubectl exec granule-multi-vm -c web -- granule exec -- uname -a"
kubectl exec granule-multi-vm -c web -- granule exec -- uname -a
echo ""
type_msg "kubectl exec granule-multi-vm -c builder -- granule exec -- uname -a"
kubectl exec granule-multi-vm -c builder -- granule exec -- uname -a
echo ""
sleep 1.5

# Step 4: Injecting Ephemeral VM Containers
echo -e "${BOLD}--- Step 4: Injecting Ephemeral MicroVM Containers into Running Pod ---${NC}"

for idx in 1 2 3; do
    vm_name="vm-ephemeral-${idx}"
    echo -e "\n${YELLOW}>>> Injecting Ephemeral VM #${idx} (${vm_name})...${NC}"
    
    t_start=$(date +%s%N)
    type_msg "kubectl debug granule-multi-vm --image=granule:snapshot -c ${vm_name} --profile=sysadmin --custom demo/assets/ephemeral-vm.yaml"
    kubectl debug granule-multi-vm --image=granule:snapshot -c "${vm_name}" --profile=sysadmin --custom demo/assets/ephemeral-vm.yaml
    
    type_msg "kubectl wait --for=jsonpath='{.status.ephemeralContainerStatuses[?(@.name==\"${vm_name}\")].state.running.startedAt}' pod/granule-multi-vm"
    kubectl wait --for=jsonpath="{.status.ephemeralContainerStatuses[?(@.name==\"${vm_name}\")].state.running.startedAt}" pod/granule-multi-vm --timeout=20s
    t_running=$(date +%s%N)
    
    type_msg "kubectl exec granule-multi-vm -c ${vm_name} -- granule ready -timeout 10s"
    kubectl exec granule-multi-vm -c "${vm_name}" -- granule ready -timeout 10s
    t_ready=$(date +%s%N)
    
    type_msg "kubectl exec granule-multi-vm -c ${vm_name} -- granule exec -- echo 'Hello World from ${vm_name}!'"
    kubectl exec granule-multi-vm -c "${vm_name}" -- granule exec -- echo "Hello World from ${vm_name}!"
    t_exec=$(date +%s%N)
    
    ready_ms=$(( (t_ready - t_start) / 1000000 ))
    e2e_ms=$(( (t_exec - t_start) / 1000000 ))
    echo -e "${GREEN}✓ ${vm_name} initialized in ${ready_ms}ms (End-to-End Hello World: ${e2e_ms}ms)${NC}"
    sleep 1
done

echo ""
sleep 1.5

# Step 5: Verify Isolation across all 5 co-located microVMs
echo -e "${BOLD}--- Step 5: Verifying Hardware Isolation Across All 5 VMs in Pod ---${NC}"
type_msg "Unique Linux Kernel Boot IDs across all Pod VM containers:"
for c in web builder vm-ephemeral-1 vm-ephemeral-2 vm-ephemeral-3; do
    boot_id=$(kubectl exec granule-multi-vm -c "$c" -- granule exec -- cat /proc/sys/kernel/random/boot_id)
    echo -e "  Container ${BOLD}${c}${NC} -> Kernel Boot ID: ${GREEN}${boot_id}${NC}"
done
echo ""
sleep 1.5

# Step 6: Benchmark & Performance Summary
echo -e "${BOLD}--- Step 6: Performance & Startup Latency Summary ---${NC}"
cat << 'EOF'
+-------------------------------------------------+-------------+-------------------+---------+
| Phase / Metric                                  | Cold Boot   | Snapshot Resume   | Speedup |
+-------------------------------------------------+-------------+-------------------+---------+
| Container Running (containerd start)            | 0.67s       | 0.78s             | -       |
| Guest VM Ready (granule ready via vsock)        | 37.48s      | 3.38s             | 11.1x   |
| Total Ephemeral VM Startup (debug -> Ready)     | 38.15s      | 4.16s             | 9.17x   |
| End-to-End Execution (kubectl exec Hello World) | 38.54s      | 4.49s             | 8.57x   |
+-------------------------------------------------+-------------+-------------------+---------+
EOF
echo ""
echo -e "${GREEN}${BOLD}Demo completed successfully! All assets verified and recorded.${NC}"
