package k8sutil

import (
	"encoding/json"

	"github.com/astaxie/beego/logs"
	"github.com/ffan/tidb-operator/pkg/util/retryutil"

	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/client-go/pkg/apis/batch/v1"
)

// CreateAndWaitJobByJSON create and wait job status 'running'
func CreateAndWaitJobByJSON(j []byte, timeout time.Duration) (*v1.Job, error) {
	job := &v1.Job{}
	if err := json.Unmarshal(j, job); err != nil {
		return nil, err
	}
	return CreateAndWaitJob(job, timeout)
}

// CreateAndWaitJob create and wait job status 'running'
func CreateAndWaitJob(job *v1.Job, timeout time.Duration) (*v1.Job, error) {
	retjob, err := kubecli.BatchV1().Jobs(Namespace).Create(job)
	if err != nil {
		return nil, err
	}

	interval := time.Second
	err = retryutil.Retry(interval, int(timeout/(interval)), func() (bool, error) {
		retjob, err = kubecli.BatchV1().Jobs(Namespace).Get(job.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		switch retjob.Status.Active {
		case 1:
			return true, nil
		default:
			return false, nil
		}
	})
	logs.Info("Job '%s' created", retjob.GetName())
	return retjob, err
}

// DeleteJob delete a job by name
func DeleteJob(name string) error {
	err := kubecli.BatchV1().Jobs(Namespace).Delete(name, &metav1.DeleteOptions{})
	if !apierrors.IsNotFound(err) {
		return err
	}
	return DeletePodsByLabel(map[string]string{"job-name": name})
}
