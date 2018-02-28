package replayer

import (
	"fmt"

	"github.com/fabric8-services/fabric8-jenkins-proxy/internal/clients"
	"github.com/fabric8-services/fabric8-jenkins-proxy/internal/storage"
	log "github.com/sirupsen/logrus"
	"net/http"
	"time"
)

type Replayer struct {
	idler           *clients.Idler
	storageService  storage.Store
	maxRequestRetry int
}

//ProcessBuffer is a loop running through buffered webhook requests trying to replay them
func (r *Replayer) ProcessBuffer() {
	for {
		namespaces, err := r.storageService.GetUsers()
		if err != nil {
			log.Error(err)
		} else {
			for _, ns := range namespaces {
				reqs, err := r.storageService.GetRequests(ns)
				if err != nil {
					log.Error(err)
					continue
				}
				for _, cachedRequest := range reqs {
					log.WithField("ns", ns).Info("Retrying request")
					isIdle, err := r.idler.IsIdle(ns)
					if err != nil {
						log.Error(err)
						break
					}
					err = r.RecordStatistics(ns, 0, time.Now().Unix())
					if err != nil {
						log.Error(err)
					}
					if !isIdle {
						req, err := cachedRequest.GetHTTPRequest()
						if err != nil {
							log.Errorf("Could not format request %s (%s): %s - deleting", cachedRequest.ID, cachedRequest.Namespace, err)
							err = r.storageService.DeleteRequest(&cachedRequest)
							if err != nil {
								log.Errorf(storage.ErrorFailedDelete, cachedRequest.ID, cachedRequest.Namespace, err)
							}
							break
						}
						client := http.DefaultClient
						if cachedRequest.Retries < r.maxRequestRetry { //Check how many times we retired (since the Jenkins started)
							resp, err := client.Do(req)
							if err != nil {
								log.Error("Error: ", err)
								errs := r.storageService.IncRequestRetry(&cachedRequest)
								if len(errs) > 0 {
									for _, e := range errs {
										log.Error(e)
									}
								}
								break
							}

							if resp.StatusCode != 200 && resp.StatusCode != 404 { //Retry later if the response is not 200
								log.Error(fmt.Sprintf("Got status %s after retrying request on %s", resp.Status, req.URL))
								errs := r.storageService.IncRequestRetry(&cachedRequest)
								if len(errs) > 0 {
									for _, e := range errs {
										log.Error(e)
									}
								}
								break
							} else if resp.StatusCode == 404 || resp.StatusCode == 400 { //400 - missing payload
								log.Warn(fmt.Sprintf("Got status %s after retrying request on %s, throwing away the request", resp.Status, req.URL))
							}

							log.WithField("ns", ns).Infof(fmt.Sprintf("Request for %s to %s forwarded.", ns, req.Host))
						}

						//Delete request if we tried too many times or the replay was successful
						err = r.storageService.DeleteRequest(&cachedRequest)
						if err != nil {
							log.Errorf(storage.ErrorFailedDelete, cachedRequest.ID, cachedRequest.Namespace, err)
						}
					} else {
						//Do not try other requests for user if Jenkins is not running
						break
					}
				}
			}
		}
		//time.Sleep(p.bufferCheckSleep * time.Second)
	}
}

//RecordStatistics writes usage statistics to a database
func (r *Replayer) RecordStatistics(ns string, la int64, lbf int64) (err error) {
	log.WithField("ns", ns).Debug("Recording stats")
	s, notFound, err := r.storageService.GetStatisticsUser(ns)
	if err != nil {
		log.WithFields(
			log.Fields{
				"ns": ns,
			}).Warningf("Could not load statistics: %s", err)
		if !notFound {
			return
		}
	}

	if notFound {
		log.WithField("ns", ns).Infof("New user %s", ns)
		s = storage.NewStatistics(ns, la, lbf)
		err = r.storageService.CreateStatistics(s)
		if err != nil {
			log.Errorf("Could not create statistics for %s: %s", ns, err)
		}
		return
	}

	if la != 0 {
		s.LastAccessed = la
	}

	if lbf != 0 {
		s.LastBufferedRequest = lbf
	}

	err = r.storageService.UpdateStatistics(s)
	if err != nil {
		log.WithField("ns", ns).Errorf("Could not update statistics for %s: %s", ns, err)
	}

	return
}
