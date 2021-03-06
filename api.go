package main

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/net/proxy"

	log "github.com/Sirupsen/logrus"
	"github.com/gorilla/mux"
)

type upstream struct {
	prefix string
	nodes  []url.URL
	dialer proxy.Dialer
}

func NewUpstream(prefix string, nodes []url.URL, dialer proxy.Dialer) *upstream {
	for _, u := range nodes {
		log.Infof("Node: %s", u.String())
	}
	return &upstream{
		prefix: prefix,
		nodes:  nodes,
		dialer: dialer,
	}
}

func (u *upstream) ServeReverseProxy(w http.ResponseWriter, req *http.Request) {
	if len(u.nodes) <= 0 {
		writeError(w, http.StatusBadGateway)
		return
	}

	node := u.nodes[rand.Intn(len(u.nodes))]
	director := func(req *http.Request) {
		req.URL.Scheme = node.Scheme
		req.URL.Host = node.Host
		req.URL.Path = strings.TrimPrefix(req.URL.Path, u.prefix)
		req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, u.prefix)
		req.Host = node.Host
		log.Infof("--> %s", req.URL.String())
	}
	res := &responseWithStatus{ResponseWriter: w}
	handler := &httputil.ReverseProxy{
		Director:  director,
		Transport: &http.Transport{Dial: u.dialer.Dial},
	}
	handler.ServeHTTP(res, req)
}

func (u *upstream) ServeNode(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	node := vars["node"]

	nodeURL, err := url.Parse(node)
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return
	}

	if req.Method == "PUT" {
		for _, node := range u.nodes {
			if node.String() == nodeURL.String() {
				return
			}
		}
		r, err := http.Get(nodeURL.String() + "/version")
		if err != nil {
			writeError(w, http.StatusBadGateway)
			return
		}
		r.Body.Close()

		u.nodes = append(u.nodes, *nodeURL)
	} else if req.Method == "DELETE" {
		for i, node := range u.nodes {
			if node.String() == nodeURL.String() {
				u.nodes = append(u.nodes[:i], u.nodes[i+1:]...)
				return
			}
		}
	} else {
		writeError(w, http.StatusMethodNotAllowed)
	}
}

func getIps(host string) []string {
	ips, err := net.LookupHost(strings.Split(host, ":")[0])
	if err != nil {
		return []string{}
	}
	return ips
}

func (u *upstream) ServeVersions(w http.ResponseWriter, req *http.Request) {
	result := make([]map[string]interface{}, 0)
	resps := u.getFromNodes("/version", false)
	for _, resp := range resps {
		var version string
		buf, err := ioutil.ReadAll(resp.Body)
		if err == nil && resp.StatusCode < 300 {
			version = strings.TrimSpace(string(buf))
		} else {
			version = "no version"
		}
		resp.Body.Close()

		url := *resp.Request.URL
		url.Path = ""
		url.RawPath = ""
		url.RawQuery = ""
		url.Fragment = ""
		result = append(result, map[string]interface{}{
			"node":    resp.Request.URL.Host,
			"url":     url.String(),
			"ips":     getIps(resp.Request.URL.Host),
			"version": version,
		})
	}

	j, err := json.Marshal(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}

	writeJson(w, j)
}

func (u *upstream) ServeSettings(w http.ResponseWriter, req *http.Request) {
	result := make(map[string]map[string]string)
	resps := u.getFromNodes("/settings", true)
	for _, resp := range resps {
		settings := make(map[string]string)
		decoder := json.NewDecoder(resp.Body)
		err := decoder.Decode(&settings)
		resp.Body.Close()
		if err != nil {
			continue
		}

		for k, v := range settings {
			if _, ok := result[k]; !ok {
				result[k] = make(map[string]string)
			}
			result[k][resp.Request.URL.Host] = v
		}
	}

	j, err := json.Marshal(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}

	writeJson(w, j)
}

func (u *upstream) deleteRoutings(queueName string) {
	node := u.nodes[rand.Intn(len(u.nodes))]
	r, err := http.Get(node.String() + "/routings")
	if err != nil {
		return
	}
	defer r.Body.Close()

	var routings []map[string]string
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&routings); err != nil {
		return
	}

	for _, r := range routings {
		queue := r["queue_name"]
		category := r["job_category"]
		if queue != queueName {
			continue
		}

		buf := make([]byte, 0)
		req, err := http.NewRequest("DELETE", node.String()+"/routing/"+url.QueryEscape(category), bytes.NewBuffer(buf))
		if err != nil {
			continue
		}
		client := &http.Client{}
		client.Do(req)
	}
}

func (u *upstream) ServeQueue(w http.ResponseWriter, req *http.Request) {
	if req.Method == "DELETE" {
		vars := mux.Vars(req)
		u.deleteRoutings(vars["queue"])
	}

	u.ServeReverseProxy(w, req)
}

func (u *upstream) ServeQueueListStats(w http.ResponseWriter, req *http.Request) {
	result := make(map[string]map[string]int64)

	resps := u.getFromNodes("/queues/stats", true)
	for _, resp := range resps {
		stats := make(map[string]map[string]int64)
		decoder := json.NewDecoder(resp.Body)
		err := decoder.Decode(&stats)
		resp.Body.Close()
		if err != nil {
			continue
		}

		for q, s := range stats {
			if _, ok := result[q]; !ok {
				result[q] = make(map[string]int64)
			}
			mergeStats(result[q], s)
		}
	}

	j, err := json.Marshal(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}

	writeJson(w, j)
}

func (u *upstream) ServeQueueStats(w http.ResponseWriter, req *http.Request) {
	vars := mux.Vars(req)
	name := vars["queue"]

	result := make(map[string]int64)
	resps := u.getFromNodes("/queue/"+url.QueryEscape(name)+"/stats", true)
	for _, resp := range resps {
		stats := make(map[string]int64)
		decoder := json.NewDecoder(resp.Body)
		err := decoder.Decode(&stats)
		resp.Body.Close()
		if err != nil {
			continue
		}
		mergeStats(result, stats)
	}

	j, err := json.Marshal(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}

	writeJson(w, j)
}

func (u *upstream) getFromNodes(path string, ignoreError bool) []*http.Response {
	resps := make([]*http.Response, 0, len(u.nodes))

	var wg sync.WaitGroup
	for _, origin := range u.nodes {
		wg.Add(1)
		go func(origin url.URL) {
			defer wg.Done()
			url := origin
			url.Path = path
			resp, err := http.Get(url.String())
			if err == nil && (resp.StatusCode < 300 || !ignoreError) {
				resps = append(resps, resp)
			}
		}(origin)
	}
	wg.Wait()

	return resps
}

func writeJson(w http.ResponseWriter, json []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(json)))
	w.WriteHeader(200)
	w.Write(json)
}

func writeError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

type responseWithStatus struct {
	http.ResponseWriter
	status int
}

func (r *responseWithStatus) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func mergeStats(stats1 map[string]int64, stats2 map[string]int64) {
	for k, v := range stats2 {
		stats1[k] += v
	}
}
