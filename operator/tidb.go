package operator

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/pkg/api/v1"

	"github.com/astaxie/beego/logs"

	"github.com/ffan/tidb-operator/pkg/util/k8sutil"
	"github.com/ffan/tidb-operator/pkg/util/retryutil"

	"github.com/ffan/tidb-operator/pkg/util/httputil"
	"github.com/ghodss/yaml"
)

var (
	defaultTidbStatusPort = 10080
)

func (td *Tidb) upgrade() error {
	var (
		err      error
		upgraded = false
		newImage = fmt.Sprintf("%s/tidb:%s", imageRegistry, td.Version)
	)

	e := NewEvent(td.Db.GetName(), "tidb/tidb", "upgrate")
	defer func() {
		td.cur = ""
		if upgraded || err != nil {
			e.Trace(err, fmt.Sprintf("Upgrate tidb to version: %s", td.Version))
			logs.Info("end upgrading", td.Db.GetName())
		}
	}()

	if td.Db.Status.Phase < PhaseTidbStarted {
		err = ErrUnavailable
		return err
	}

	err = upgradeRC("tidb-"+td.Db.GetName(), newImage, td.Version)
	if err != nil {
		return err
	}
	// get tidb pods
	pods, err := k8sutil.GetPods(td.Db.GetName(), "tidb")
	if err != nil {
		return err
	}
	for i := range pods {
		pod := pods[i]
		if needUpgrade(&pod, td.Version) {
			upgraded = true
			// delete pod, rc will create a new version pod
			if err = k8sutil.DeletePods(pod.GetName()); err != nil {
				return err
			}
			// sleep terminationGracePeriodSeconds
			time.Sleep(8 * time.Second)

			td.cur = pod.GetName()
			if err = td.waitForOk(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (td *Tidb) install() (err error) {
	td.Db.Status.Phase = PhaseTidbPending
	if err = td.Db.update(); err != nil {
		return err
	}

	e := NewEvent(td.Db.GetName(), "tidb/tidb", "install")
	defer func() {
		ph := PhaseTidbStarted
		if err != nil {
			ph = PhaseTidbStartFailed
		}
		td.Db.Status.Phase = ph

		e.Trace(err, fmt.Sprintf("Install tidb replicationcontrollers with %d replicas on k8s", td.Replicas))
	}()

	if err = td.createService(); err != nil {
		return err
	}
	if err = td.createReplicationController(); err != nil {
		return err
	}

	// wait tidb started
	if err = td.waitForOk(); err != nil {
		return err
	}
	return nil
}

func (td *Tidb) syncMembers() error {
	pods, err := k8sutil.ListPodNames(td.Db.GetName(), "tidb")
	if err != nil {
		return err
	}
	td.Members = nil
	for _, n := range pods {
		td.Members = append(td.Members, &Member{Name: n})
	}
	return nil
}

func (td *Tidb) createService() (err error) {
	j, err := td.toJSONTemplate(tidbServiceYaml)
	if err != nil {
		return err
	}
	srv, err := k8sutil.CreateServiceByJSON(j)
	if err != nil {
		return err
	}
	ps := getProxys()
	for _, py := range ps {
		td.Db.Status.OuterAddresses =
			append(td.Db.Status.OuterAddresses, fmt.Sprintf("%s:%d", py, srv.Spec.Ports[0].NodePort))
	}
	td.Db.Status.OuterStatusAddresses =
		append(td.Db.Status.OuterStatusAddresses, fmt.Sprintf("%s:%d", ps[0], srv.Spec.Ports[1].NodePort))
	return nil
}

func (td *Tidb) createReplicationController() error {
	var (
		err error
		j   []byte
	)
	j, err = td.toJSONTemplate(tidbRcYaml)
	if err != nil {
		return err
	}
	_, err = k8sutil.CreateRcByJSON(j, waitPodRuningTimeout, func(rc *v1.ReplicationController) {
		k8sutil.SetTidbVersion(rc, td.Version)
	})
	return err
}

func (td *Tidb) toJSONTemplate(temp string) ([]byte, error) {
	r := strings.NewReplacer(
		"{{version}}", td.Version,
		"{{cpu}}", fmt.Sprintf("%v", td.CPU), "{{mem}}", fmt.Sprintf("%v", td.Mem),
		"{{namespace}}", getNamespace(),
		"{{replicas}}", fmt.Sprintf("%v", td.Replicas),
		"{{registry}}", imageRegistry, "{{cell}}", td.Db.Metadata.Name)
	str := r.Replace(temp)
	j, err := yaml.YAMLToJSON([]byte(str))
	if err != nil {
		return nil, err
	}
	return j, nil
}

func (td *Tidb) waitForOk() (err error) {
	logs.Debug("waiting for tidb %q running...", td.Db.GetName())
	host := td.Db.Status.OuterStatusAddresses[0]
	// for upgrade check
	if td.cur != "" {
		host = fmt.Sprintf("%s:%d", td.cur, defaultTidbStatusPort)
	}
	sURL := fmt.Sprintf("http://%s/status", host)
	interval := 3 * time.Second
	err = retryutil.Retry(interval, int(waitTidbComponentAvailableTimeout/(interval)), func() (bool, error) {
		// check pod

		pods, err := k8sutil.GetPods(td.Db.GetName(), "tidb")
		if err != nil {
			return false, err
		}
		count := 0
		for _, pod := range pods {
			if k8sutil.IsPodOk(pod) {
				count++
			}
		}
		if count != td.Replicas {
			logs.Warn("some pods(tidb-%s-*) not running yet", td.Db.GetName())
			return false, nil
		}

		// check tidb status

		if _, err := httputil.Get(sURL, 2*time.Second); err != nil {
			logs.Warn("could not get tidb status: %v", err)
			return false, nil
		}
		err = td.syncMembers()
		if err != nil {
			return false, err
		}

		return true, nil
	})
	if err != nil {
		logs.Error("wait tidb %q available: %v", td.Db.GetName(), err)
	} else {
		logs.Debug("tidb %q ok", td.Db.GetName())
	}
	return err
}

func (td *Tidb) uninstall() (err error) {
	if err = k8sutil.DelRc(fmt.Sprintf("tidb-%s", td.Db.GetName())); err != nil {
		return err
	}
	if err = k8sutil.DelSrvs(fmt.Sprintf("tidb-%s", td.Db.GetName())); err != nil {
		return err
	}
	td.Members = nil
	td.cur = ""
	td.Db.Status.MigrateState = ""
	td.Db.Status.ScaleState = 0
	td.Db.Status.OuterAddresses = nil
	td.Db.Status.OuterStatusAddresses = nil

	return nil
}

func (db *Db) reconcileTidbs(replica int) error {
	var (
		err error
		td  = db.Tidb
	)

	if replica < 1 || replica == db.Tidb.Replicas {
		if err = td.checkStatus(); err != nil {
			return err
		}
		return nil
	}

	// update status

	e := NewEvent(db.GetName(), "tidb/tidb", "scale")
	defer func(r int) {
		if err != nil {
			db.Status.ScaleState |= tidbScaleErr
		}
		e.Trace(err, fmt.Sprintf("Scale tidb '%s' replicas from %d to %d", db.GetName(), r, replica))
	}(td.Replicas)

	// check replicas

	md := getCachedMetadata()
	if replica > md.Spec.Tidb.Max {
		err = fmt.Errorf("the replicas of tidb exceeds max %d", md.Spec.Tidb.Max)
		return err
	}
	if replica < 2 {
		err = fmt.Errorf("replicas must be greater than 2")
		return err
	}
	if replica > td.Replicas*3 {
		err = fmt.Errorf("each scale out can not more then 2 times")
		return err
	}
	if (td.Spec.Replicas-replica)*3 > td.Spec.Replicas {
		err = fmt.Errorf("each scale dowm can not be less than one-third")
		return err
	}

	// scale

	logs.Info("start scaling tidb count of the db '%s' from %d to %d",
		db.GetName(), td.Replicas, replica)
	td.Replicas = replica
	if err = k8sutil.ScaleReplicationController(fmt.Sprintf("tidb-%s", db.GetName()), replica); err != nil {
		return err
	}
	if err = td.waitForOk(); err != nil {
		return err
	}

	return nil
}

func (td *Tidb) checkStatus() error {
	pods, err := k8sutil.GetPods(td.Db.GetName(), "tidb")
	if err != nil {
		return err
	}
	for i := range pods {
		pod := pods[i]
		if !k8sutil.IsPodOk(pod) {
			err = k8sutil.DeletePods(pod.GetName())
			if err != nil {
				return err
			}
			continue
		}
	}
	return nil
}
