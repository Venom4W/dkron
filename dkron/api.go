package dkron

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/carbocation/interpose"
	"github.com/gorilla/mux"
	"io"
	"io/ioutil"
)

func (a *AgentCommand) ServeHTTP() {
	r := mux.NewRouter().StrictSlash(true)

	a.apiRoutes(r)
	a.dashboardRoutes(r)

	middle := interpose.New()
	middle.UseHandler(r)

	// Path of static files must be last!
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("static")))

	srv := &http.Server{Addr: a.config.HTTPAddr, Handler: middle}

	log.Infof("Running HTTP server on %s", a.config.HTTPAddr)

	certFile := "" //config.GetString("certFile")
	keyFile := ""  //config.GetString("keyFile")
	if certFile != "" && keyFile != "" {
		srv.ListenAndServeTLS(certFile, keyFile)
	} else {
		srv.ListenAndServe()
	}
	log.Debug("Exiting HTTP server")
}

func (a *AgentCommand) apiRoutes(r *mux.Router) {
	r.Path("/v1").HandlerFunc(a.indexHandler)
	subver := r.PathPrefix("/v1").Subrouter()
	subver.HandleFunc("/members", a.membersHandler)
	subver.HandleFunc("/leader", a.leaderHandler)

	subver.Path("/jobs").HandlerFunc(a.jobCreateOrUpdateHandler).Methods("POST", "PATCH")
	subver.Path("/jobs").HandlerFunc(a.jobsHandler).Methods("GET")
	sub := subver.PathPrefix("/jobs").Subrouter()
	sub.HandleFunc("/{job}", a.jobGetHandler).Methods("GET")
	sub.HandleFunc("/{job}", a.jobDeleteHandler).Methods("DELETE")
	sub.HandleFunc("/{job}", a.jobRunHandler).Methods("POST")

	subex := subver.PathPrefix("/executions").Subrouter()
	subex.HandleFunc("/{job}", a.executionsHandler).Methods("GET")
}

func (a *AgentCommand) indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)

	local := a.serf.LocalMember()
	stats := map[string]map[string]string{
		"agent": {
			"name":    local.Name,
			"version": a.Version,
		},
		"serf": a.serf.Stats(),
		"tags": local.Tags,
	}

	statsJson, _ := json.MarshalIndent(stats, "", "\t")
	if _, err := fmt.Fprintf(w, string(statsJson)); err != nil {
		log.Fatal(err)
	}
}

func (a *AgentCommand) jobsHandler(w http.ResponseWriter, r *http.Request) {
	jobs, err := a.etcd.GetJobs()
	if err != nil {
		log.Error(err)
	}
	log.Debug(jobs)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(jobs); err != nil {
		log.Fatal(err)
	}
}

func (a *AgentCommand) jobGetHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobName := vars["job"]

	job, err := a.etcd.GetJob(jobName)
	if err != nil {
		log.Error(err)
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(job); err != nil {
		log.Fatal(err)
	}
}

func (a *AgentCommand) jobCreateOrUpdateHandler(w http.ResponseWriter, r *http.Request) {
	var job Job
	body, err := ioutil.ReadAll(io.LimitReader(r.Body, 1048576))
	if err != nil {
		log.Fatal(err)
	}
	if err := r.Body.Close(); err != nil {
		log.Fatal(err)
	}

	if err := json.Unmarshal(body, &job); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(422) // unprocessable entity
		if err := json.NewEncoder(w).Encode(err); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Save the new job to etcd
	if err = a.etcd.SetJob(&job); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(422) // unprocessable entity
		if err := json.NewEncoder(w).Encode(err); err != nil {
			log.Fatal(err)
		}
		return
	}

	a.schedulerRestartQuery(a.etcd.GetLeader())

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(job); err != nil {
		log.Fatal(err)
	}
}

func (a *AgentCommand) jobDeleteHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobName := vars["job"]

	job, err := a.etcd.DeleteJob(jobName)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusNotFound)
		if err := json.NewEncoder(w).Encode(err); err != nil {
			log.Fatal(err)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(job); err != nil {
		log.Fatal(err)
	}
}

func (a *AgentCommand) jobRunHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobName := vars["job"]

	job, err := a.etcd.GetJob(jobName)
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusNotFound)
		if err := json.NewEncoder(w).Encode(err); err != nil {
			log.Fatal(err)
		}
		return
	}

	a.RunQuery(job)

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(job); err != nil {
		log.Fatal(err)
	}
}

func (a *AgentCommand) executionsHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	jobName := vars["job"]

	executions, err := a.etcd.GetExecutions(jobName)
	if err != nil {
		log.Error(err)
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(executions); err != nil {
		panic(err)
	}
}

func (a *AgentCommand) membersHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(a.serf.Members()); err != nil {
		log.Fatal(err)
	}
}

func (a *AgentCommand) leaderHandler(w http.ResponseWriter, r *http.Request) {
	member, err := a.leaderMember()
	if err == nil {
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(member); err != nil {
			log.Fatal(err)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(http.StatusNotFound)
	if err := json.NewEncoder(w).Encode(err); err != nil {
		log.Fatal(err)
	}
}