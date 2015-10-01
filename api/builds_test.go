package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"sync"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/ghttp"
	"github.com/pivotal-golang/lager"

	"github.com/concourse/atc"
	"github.com/concourse/atc/db"
	enginefakes "github.com/concourse/atc/engine/fakes"
)

var _ = Describe("Builds API", func() {
	Describe("POST /api/v1/builds", func() {
		var plan atc.Plan

		var response *http.Response

		BeforeEach(func() {
			plan = atc.Plan{
				Task: &atc.TaskPlan{
					Config: &atc.TaskConfig{
						Run: atc.TaskRunConfig{
							Path: "ls",
						},
					},
				},
			}
		})

		JustBeforeEach(func() {
			reqPayload, err := json.Marshal(plan)
			Expect(err).NotTo(HaveOccurred())

			req, err := http.NewRequest("POST", server.URL+"/api/v1/builds", bytes.NewBuffer(reqPayload))
			Expect(err).NotTo(HaveOccurred())

			req.Header.Set("Content-Type", "application/json")

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				authValidator.IsAuthenticatedReturns(true)
			})

			Context("when creating a one-off build succeeds", func() {
				BeforeEach(func() {
					buildsDB.CreateOneOffBuildReturns(db.Build{
						ID:           42,
						Name:         "1",
						JobName:      "job1",
						PipelineName: "some-pipeline",
						Status:       db.StatusStarted,
					}, nil)
				})

				Context("and building succeeds", func() {
					var fakeBuild *enginefakes.FakeBuild
					var blockForever *sync.WaitGroup

					BeforeEach(func() {
						fakeBuild = new(enginefakes.FakeBuild)

						blockForever = new(sync.WaitGroup)

						forever := blockForever
						forever.Add(1)

						fakeBuild.ResumeStub = func(lager.Logger) {
							forever.Wait()
						}

						fakeEngine.CreateBuildReturns(fakeBuild, nil)
					})

					AfterEach(func() {
						blockForever.Done()
					})

					It("returns 201 Created", func() {
						Expect(response.StatusCode).To(Equal(http.StatusCreated))
					})

					It("returns the build", func() {
						body, err := ioutil.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(body).To(MatchJSON(`{
							"id": 42,
							"name": "1",
							"job_name": "job1",
							"status": "started",
							"url": "/pipelines/some-pipeline/jobs/job1/builds/1"
						}`))

					})

					It("creates a one-off build and runs it asynchronously", func() {
						Expect(buildsDB.CreateOneOffBuildCallCount()).To(Equal(1))

						Expect(fakeEngine.CreateBuildCallCount()).To(Equal(1))
						oneOff, builtPlan := fakeEngine.CreateBuildArgsForCall(0)
						Expect(oneOff).To(Equal(db.Build{
							ID:           42,
							Name:         "1",
							JobName:      "job1",
							PipelineName: "some-pipeline",
							Status:       db.StatusStarted,
						}))

						Expect(builtPlan).To(Equal(plan))

						Expect(fakeBuild.ResumeCallCount()).To(Equal(1))
					})
				})

				Context("and building fails", func() {
					BeforeEach(func() {
						fakeEngine.CreateBuildReturns(nil, errors.New("oh no!"))
					})

					It("returns 500 Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when creating a one-off build fails", func() {
				BeforeEach(func() {
					buildsDB.CreateOneOffBuildReturns(db.Build{}, errors.New("oh no!"))
				})

				It("returns 500 Internal Server Error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				authValidator.IsAuthenticatedReturns(false)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

			It("does not trigger a build", func() {
				Expect(buildsDB.CreateOneOffBuildCallCount()).To(BeZero())
				Expect(fakeEngine.CreateBuildCallCount()).To(BeZero())
			})
		})
	})

	Describe("GET /api/v1/builds/:build_id", func() {
		var response *http.Response

		Context("when parsing the build_id fails", func() {
			BeforeEach(func() {
				var err error

				response, err = client.Get(server.URL + "/api/v1/builds/nope")
				Expect(err).NotTo(HaveOccurred())
			})

			It("returns Bad Request", func() {
				Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
			})
		})

		Context("when parsing the build_id succeeds", func() {
			JustBeforeEach(func() {
				var err error

				response, err = client.Get(server.URL + "/api/v1/builds/1")
				Expect(err).NotTo(HaveOccurred())
			})

			Context("when calling the database fails", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{}, false, errors.New("disaster"))
				})

				It("returns 500 Internal Server Error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})

			Context("when the build cannot be found", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{}, false, nil)
				})

				It("returns Not Found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when the build can be found", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{
						ID:           1,
						Name:         "1",
						JobName:      "job1",
						PipelineName: "some-pipeline",
						Status:       db.StatusSucceeded,
					}, true, nil)
				})

				It("returns 200 OK", func() {
					Expect(response.StatusCode).To(Equal(http.StatusOK))
				})

				It("returns the build with the given build_id", func() {
					Expect(buildsDB.GetBuildCallCount()).To(Equal(1))
					buildID := buildsDB.GetBuildArgsForCall(0)
					Expect(buildID).To(Equal(1))

					body, err := ioutil.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(body).To(MatchJSON(`{
						"id": 1,
						"name": "1",
						"status": "succeeded",
						"job_name": "job1",
						"url": "/pipelines/some-pipeline/jobs/job1/builds/1"
					}`))

				})
			})
		})
	})

	Describe("GET /api/v1/builds", func() {
		var response *http.Response

		JustBeforeEach(func() {
			var err error

			response, err = client.Get(server.URL + "/api/v1/builds")
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when getting all builds succeeds", func() {
			BeforeEach(func() {
				buildsDB.GetAllBuildsReturns([]db.Build{
					{
						ID:           3,
						Name:         "2",
						JobName:      "job2",
						PipelineName: "some-pipeline",
						Status:       db.StatusStarted,
					},
					{
						ID:           1,
						Name:         "1",
						JobName:      "job1",
						PipelineName: "some-pipeline",
						Status:       db.StatusSucceeded,
					},
				}, nil)
			})

			It("returns 200 OK", func() {
				Expect(response.StatusCode).To(Equal(http.StatusOK))
			})

			It("returns all builds", func() {
				body, err := ioutil.ReadAll(response.Body)
				Expect(err).NotTo(HaveOccurred())

				Expect(body).To(MatchJSON(`[
					{
						"id": 3,
						"name": "2",
						"job_name": "job2",
						"status": "started",
						"url": "/pipelines/some-pipeline/jobs/job2/builds/2"
					},
					{
						"id": 1,
						"name": "1",
						"job_name": "job1",
						"status": "succeeded",
						"url": "/pipelines/some-pipeline/jobs/job1/builds/1"
					}
				]`))

			})
		})

		Context("when getting all builds fails", func() {
			BeforeEach(func() {
				buildsDB.GetAllBuildsReturns(nil, errors.New("oh no!"))
			})

			It("returns 500 Internal Server Error", func() {
				Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
			})
		})
	})

	Describe("GET /api/v1/builds/:build_id/events", func() {
		var (
			request  *http.Request
			response *http.Response
		)

		BeforeEach(func() {
			var err error

			request, err = http.NewRequest("GET", server.URL+"/api/v1/builds/128/events", nil)
			Expect(err).NotTo(HaveOccurred())
		})

		JustBeforeEach(func() {
			var err error

			response, err = client.Do(request)
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				authValidator.IsAuthenticatedReturns(true)
			})

			Context("when the build can be found", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{
						ID:      128,
						JobName: "some-job",
					}, true, nil)
				})

				It("returns 200", func() {
					Expect(response.StatusCode).To(Equal(200))
				})

				It("serves the request via the event handler", func() {
					body, err := ioutil.ReadAll(response.Body)
					Expect(err).NotTo(HaveOccurred())

					Expect(string(body)).To(Equal("fake event handler factory was here"))

					Expect(constructedEventHandler.db).To(Equal(buildsDB))
					Expect(constructedEventHandler.buildID).To(Equal(128))
				})
			})

			Context("when the build can not be found", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{}, false, nil)
				})

				It("returns Not Found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when calling the database fails", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{}, false, errors.New("nope"))
				})

				It("returns Internal Server Error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				authValidator.IsAuthenticatedReturns(false)
			})

			Context("when the build can be found", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{
						ID:      128,
						JobName: "some-job",
					}, true, nil)
				})

				It("looks up the config from the buildsDB", func() {
					Expect(buildsDB.GetConfigByBuildIDCallCount()).To(Equal(1))
					buildID := buildsDB.GetConfigByBuildIDArgsForCall(0)
					Expect(buildID).To(Equal(128))
				})

				Context("and the build is private", func() {
					BeforeEach(func() {
						buildsDB.GetConfigByBuildIDReturns(atc.Config{
							Jobs: atc.JobConfigs{
								{Name: "some-job", Public: false},
							},
						}, 1, nil)
					})

					It("returns 401", func() {
						Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
					})
				})

				Context("and the build is public", func() {
					BeforeEach(func() {
						buildsDB.GetConfigByBuildIDReturns(atc.Config{
							Jobs: atc.JobConfigs{
								{Name: "some-job", Public: true},
							},
						}, 1, nil)
					})

					It("returns 200", func() {
						Expect(response.StatusCode).To(Equal(200))
					})

					It("serves the request via the event handler", func() {
						body, err := ioutil.ReadAll(response.Body)
						Expect(err).NotTo(HaveOccurred())

						Expect(string(body)).To(Equal("fake event handler factory was here"))

						Expect(constructedEventHandler.db).To(Equal(buildsDB))
						Expect(constructedEventHandler.buildID).To(Equal(128))
					})
				})
			})

			Context("when the build can not be found", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{}, false, nil)
				})

				It("returns Not Found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when calling the database fails", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{}, false, errors.New("nope"))
				})

				It("returns Internal Server Error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})
	})

	Describe("POST /api/v1/builds/:build_id/abort", func() {
		var (
			abortTarget *ghttp.Server

			response *http.Response
		)

		BeforeEach(func() {
			abortTarget = ghttp.NewServer()

			abortTarget.AppendHandlers(
				ghttp.VerifyRequest("POST", "/builds/some-guid/abort"),
			)
		})

		JustBeforeEach(func() {
			var err error

			req, err := http.NewRequest("POST", server.URL+"/api/v1/builds/128/abort", nil)
			Expect(err).NotTo(HaveOccurred())

			response, err = client.Do(req)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			abortTarget.Close()
		})

		Context("when authenticated", func() {
			BeforeEach(func() {
				authValidator.IsAuthenticatedReturns(true)
			})

			Context("when the build can be found", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{
						ID:     128,
						Status: db.StatusStarted,
					}, true, nil)
				})

				Context("when the engine returns a build", func() {
					var fakeBuild *enginefakes.FakeBuild

					BeforeEach(func() {
						fakeBuild = new(enginefakes.FakeBuild)
						fakeEngine.LookupBuildReturns(fakeBuild, nil)
					})

					It("aborts the build", func() {
						Expect(fakeBuild.AbortCallCount()).To(Equal(1))
					})

					Context("when aborting succeeds", func() {
						BeforeEach(func() {
							fakeBuild.AbortReturns(nil)
						})

						It("returns 204", func() {
							Expect(response.StatusCode).To(Equal(http.StatusNoContent))
						})
					})

					Context("when aborting fails", func() {
						BeforeEach(func() {
							fakeBuild.AbortReturns(errors.New("oh no!"))
						})

						It("returns 500", func() {
							Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
						})
					})
				})

				Context("when the engine returns no build", func() {
					BeforeEach(func() {
						fakeEngine.LookupBuildReturns(nil, errors.New("oh no!"))
					})

					It("returns Internal Server Error", func() {
						Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
					})
				})
			})

			Context("when the build can not be found", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{}, false, nil)
				})

				It("returns Not Found", func() {
					Expect(response.StatusCode).To(Equal(http.StatusNotFound))
				})
			})

			Context("when calling the database fails", func() {
				BeforeEach(func() {
					buildsDB.GetBuildReturns(db.Build{}, false, errors.New("nope"))
				})

				It("returns Internal Server Error", func() {
					Expect(response.StatusCode).To(Equal(http.StatusInternalServerError))
				})
			})
		})

		Context("when not authenticated", func() {
			BeforeEach(func() {
				authValidator.IsAuthenticatedReturns(false)
			})

			It("returns 401", func() {
				Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
			})

			It("does not abort the build", func() {
				Expect(abortTarget.ReceivedRequests()).To(BeEmpty())
			})
		})
	})
})
