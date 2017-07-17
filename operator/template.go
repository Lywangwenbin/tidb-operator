package operator

import "github.com/tidwall/gjson"
import "fmt"
import "github.com/ghodss/yaml"

var pdServiceYaml = `
kind: Service
apiVersion: v1
metadata:
  name: pd-{{cell}}
  labels:
    component: pd
    cell: {{cell}}
    app: tidb
spec:
  ports:
    - name: client
      port: 2379
  selector:
    component: pd
    cell: {{cell}}
    app: tidb
  type: NodePort
`

var pdHeadlessServiceYaml = `
kind: Service
apiVersion: v1
metadata:
  name: pd-{{cell}}-srv
  labels:
    component: pd
    cell: {{cell}}
    app: tidb
spec:
  clusterIP: None
  ports:
    - name: pd-server
      port: 2380
  selector:
    component: pd
    cell: {{cell}}
    app: tidb
`

var pdPodYaml = `
apiVersion: v1
kind: Pod
metadata:
  name: pd-{{cell}}-{{id}}
  labels:
    component: pd
    cell: {{cell}}
    app: tidb
spec:
  volumes:
  - name: tidb-data
    {{tidbdata_volume}}
  # default is 30s
  terminationGracePeriodSeconds: 5
  restartPolicy: Always
  # DNS A record: [m.Name].[clusterName].Namespace.svc.cluster.local.
  # For example, pd-test-001 in default namesapce will have DNS name
  # 'pd-test-001.test.default.svc.cluster.local'.
  hostname: pd-{{cell}}-{{id}}
  subdomain: pd-{{cell}}-srv
  containers:
    - name: pd
      image: {{registry}}/pd:{{version}}
      # imagePullPolicy: IfNotPresent
      volumeMounts:
      - name: tidb-data
        mountPath: /var/pd
      resources:
        limits:
          memory: "{{mem}}Mi"
          cpu: "{{cpu}}m"
      env: 
      - name: M_INTERVAL
        value: "15"
      command:
        - bash
        - "-c"
        - |
          client_urls="http://0.0.0.0:2379"
          # FQDN
          advertise_client_urls="http://pd-{{cell}}-{{id}}.pd-{{cell}}-srv.{{namespace}}.svc.cluster.local:2379"
          peer_urls="http://0.0.0.0:2380"
          advertise_peer_urls="http://pd-{{cell}}-{{id}}.pd-{{cell}}-srv.{{namespace}}.svc.cluster.local:2380"

          export PD_NAME=$HOSTNAME
          export PD_DATA_DIR=/var/pd/$HOSTNAME/data

          export CLIENT_URLS=$client_urls
          export ADVERTISE_CLIENT_URLS=$advertise_client_urls
          export PEER_URLS=$peer_urls
          export ADVERTISE_PEER_URLS=$advertise_peer_urls

          # set prometheus
          sed -i -e 's/{m-job}/{{cell}}/' /etc/pd/config.toml

          if [ -d $PD_DATA_DIR ]; then
            echo "Resuming with existing data dir:$PD_DATA_DIR"
          else
            echo "First run for this member"
            # First wait for the desired number of replicas to show up.
            echo "Waiting for {{replicas}} replicas in SRV record for {{cell}}..."
            until [ $(getpods {{cell}} | wc -l) -eq {{replicas}} ]; do
              echo "[$(date)] waiting for {{replicas}} entries in SRV record for {{cell}}"
              sleep 1
            done
          fi

          urls=""
          for id in {1..{{replicas}}}; do
            id=$(printf "%03d\n" $id)
            urls+="pd-{{cell}}-${id}=http://pd-{{cell}}-${id}.pd-{{cell}}-srv.{{namespace}}.svc.cluster.local:2380,"
          done
          urls=${urls%,}
          echo "Initial-cluster:$urls"

          pd-server \
          --name="$PD_NAME" \
          --data-dir="$PD_DATA_DIR" \
          --client-urls="$CLIENT_URLS" \
          --advertise-client-urls="$ADVERTISE_CLIENT_URLS" \
          --peer-urls="$PEER_URLS" \
          --advertise-peer-urls="$ADVERTISE_PEER_URLS" \
          --initial-cluster=$urls \
          --config="/etc/pd/config.toml"
`

var tikvPodYaml = `
apiVersion: v1
kind: Pod
metadata:
  name: tikv-{{cell}}-{{id}}
  labels:
    app: tidb
    cell: {{cell}}
    component: tikv
spec:
  affinity:
    # PD and TiKV instances, it is recommended that each instance individually deploy a hard disk 
    # to avoid IO conflicts and affect performance
    podAntiAffinity:
      preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 80
        podAffinityTerm:
          labelSelector:
            matchExpressions:
            - key: component
              operator: In
              values:
              - "pd"
          topologyKey: kubernetes.io/hostname
  volumes:
    - name: syslog
      hostPath: {path: /dev/log}
    - name: datadir
      {{tidbdata_volume}}
    - name: zone
      hostPath: {path: /etc/localtime}
  terminationGracePeriodSeconds: 5
  containers:
  - name: tikv
    image: {{registry}}/tikv:{{version}}
    resources:
      # 初始化requests和limits相同的值，是为了防止memory超过requests时，node资源不足，导致该pod被重新安排到其它node
      requests:
        memory: "{{mem}}Mi"
        cpu: "{{cpu}}m"
      limits:
        memory: "{{mem}}Mi"
        cpu: "{{cpu}}m"
    ports:
    - containerPort: 20160
    volumeMounts:
      - name: datadir
        mountPath: /data
    command:
      - bash
      - "-c"
      - |
        /tikv-server \
        --store="/data/tikv-{{cell}}-{{id}}" \
        --addr="0.0.0.0:20160" \
        --capacity={{capacity}}GB \
        --advertise-addr="$POD_IP:20160" \
        --pd="pd-{{cell}}:2379" \
        --config="/etc/tikv/config.toml"
    env: 
      - name: POD_IP
        valueFrom:
          fieldRef:
            fieldPath: status.podIP
      - name: TZ
        value: "Asia/Shanghai"
`

var tidbServiceYaml = `
kind: Service
apiVersion: v1
metadata:
  name: tidb-{{cell}}
  labels:
    component: tidb
    cell: {{cell}}
    app: tidb
spec:
  ports:
    - name: mysql
      port: 4000
    - name: web
      port: 10080
  selector:
    component: tidb
    cell: {{cell}}
    app: tidb
  sessionAffinity: None
  type: NodePort
`

var tidbRcYaml = `
kind: ReplicationController
apiVersion: v1
metadata:
  name: tidb-{{cell}}
spec:
  replicas: {{replicas}}
  template:
    metadata:
      labels:
        component: tidb
        cell: {{cell}}
        app: tidb
    spec:
      affinity:
        # iDB and TiKV instances, it is recommended to deploy separately to avoid competing CPU resources and performance
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 80
            podAffinityTerm:
              labelSelector:
                matchExpressions:
                - key: component
                  operator: In
                  values:
                  - "tikv"
              topologyKey: kubernetes.io/hostname
      volumes:
        - name: syslog
          hostPath: {path: /dev/log}
      terminationGracePeriodSeconds: 5
      containers:
      - name: tidb
        image: {{registry}}/tidb:{{version}}
        livenessProbe:
          httpGet:
            path: /status
            port: 10080
          initialDelaySeconds: 30
          timeoutSeconds: 5
        volumeMounts:
          - name: syslog
            mountPath: /dev/log
        resources:
          limits:
            memory: "{{mem}}Mi"
            cpu: "{{cpu}}m"
        command: ["/tidb-server"]
        args: 
          - -P=4000
          - --store=tikv
          - --path=pd-{{cell}}:2379
          - --metrics-addr=prom-gateway:9091
          - --metrics-interval=15
        env: 
          - name: TZ
            value: "Asia/Shanghai"
`

var mysqlMigrateYaml = `
apiVersion: v1
kind: Pod
metadata:
  name: migrator-{{cell}}
  labels:
    app: tidb
    cell: {{cell}}
    component: migrator
spec:
  volumes:
    - name: syslog
      hostPath: {path: /dev/log}
  terminationGracePeriodSeconds: 10
  containers:
  - name: migrator
    image: {{image}}
    resources:
      limits:
        cpu: "200m"
        memory: "512Mi"
    command:
      - bash
      - "-c"
      - |
        migrator \
          --database {{db}} \
          --src-host {{sh}} \
          --src-port {{sP}} \
          --src-user {{su}} \
          --src-password {{sp}} \
          --dest-host {{dh}} \
          --dest-port {{dP}} \
          --dest-user {{du}} \
          --dest-password {{dp}} \
          --operator {{op}} \
          --notice "{{api}}"
        while true; do
          echo "Waiting for the pod to closed"
          sleep 60
        done
    env: 
    - name: TZ
      value: "Asia/Shanghai"
`

func getResourceName(s string) string {
	j, _ := yaml.YAMLToJSON([]byte(s))
	return fmt.Sprintf("%s", gjson.Get(string(j), "metadata.name"))
}
