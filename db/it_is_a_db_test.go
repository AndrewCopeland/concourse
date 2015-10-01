package db_test

import (
	"time"

	"github.com/concourse/atc"
	"github.com/concourse/atc/db"
	"github.com/concourse/atc/event"
	"github.com/nu7hatch/gouuid"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
)

type dbSharedBehaviorInput struct {
	db.DB
	PipelineDB db.PipelineDB
}

func dbSharedBehavior(database *dbSharedBehaviorInput) func() {
	return func() {
		Describe("CreatePipe", func() {
			It("saves a pipe to the db", func() {
				myGuid, err := uuid.NewV4()
				Expect(err).NotTo(HaveOccurred())

				err = database.CreatePipe(myGuid.String(), "a-url")
				Expect(err).NotTo(HaveOccurred())

				pipe, err := database.GetPipe(myGuid.String())
				Expect(err).NotTo(HaveOccurred())
				Expect(pipe.ID).To(Equal(myGuid.String()))
				Expect(pipe.URL).To(Equal("a-url"))
			})
		})

		It("saves and propagates events correctly", func() {
			build, err := database.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			Expect(build.Name).To(Equal("1"))

			By("allowing you to subscribe when no events have yet occurred")
			events, err := database.GetBuildEvents(build.ID, 0)
			Expect(err).NotTo(HaveOccurred())

			defer events.Close()

			By("saving them in order")
			err = database.SaveBuildEvent(build.ID, event.Log{
				Payload: "some ",
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(events.Next()).To(Equal(event.Log{
				Payload: "some ",
			}))

			err = database.SaveBuildEvent(build.ID, event.Log{
				Payload: "log",
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(events.Next()).To(Equal(event.Log{
				Payload: "log",
			}))

			By("allowing you to subscribe from an offset")
			eventsFrom1, err := database.GetBuildEvents(build.ID, 1)
			Expect(err).NotTo(HaveOccurred())

			defer eventsFrom1.Close()

			Expect(eventsFrom1.Next()).To(Equal(event.Log{
				Payload: "log",
			}))

			By("notifying those waiting on events as soon as they're saved")
			nextEvent := make(chan atc.Event)
			nextErr := make(chan error)

			go func() {
				event, err := events.Next()
				if err != nil {
					nextErr <- err
				} else {
					nextEvent <- event
				}
			}()

			Consistently(nextEvent).ShouldNot(Receive())
			Consistently(nextErr).ShouldNot(Receive())

			err = database.SaveBuildEvent(build.ID, event.Log{
				Payload: "log 2",
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(nextEvent).Should(Receive(Equal(event.Log{
				Payload: "log 2",
			})))

			By("returning ErrBuildEventStreamClosed for Next calls after Close")
			events3, err := database.GetBuildEvents(build.ID, 0)
			Expect(err).NotTo(HaveOccurred())

			events3.Close()

			_, err = events3.Next()
			Expect(err).To(Equal(db.ErrBuildEventStreamClosed))
		})

		It("saves and emits status events", func() {
			build, err := database.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			Expect(build.Name).To(Equal("1"))

			By("allowing you to subscribe when no events have yet occurred")
			events, err := database.GetBuildEvents(build.ID, 0)
			Expect(err).NotTo(HaveOccurred())

			defer events.Close()

			By("emitting a status event when started")
			started, err := database.StartBuild(build.ID, "engine", "metadata")
			Expect(err).NotTo(HaveOccurred())
			Expect(started).To(BeTrue())

			startedBuild, found, err := database.GetBuild(build.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(events.Next()).To(Equal(event.Status{
				Status: atc.StatusStarted,
				Time:   startedBuild.StartTime.Unix(),
			}))

			By("emitting a status event when finished")
			err = database.FinishBuild(build.ID, db.StatusSucceeded)
			Expect(err).NotTo(HaveOccurred())

			finishedBuild, found, err := database.GetBuild(build.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(events.Next()).To(Equal(event.Status{
				Status: atc.StatusSucceeded,
				Time:   finishedBuild.EndTime.Unix(),
			}))

			By("ending the stream when finished")
			_, err = events.Next()
			Expect(err).To(Equal(db.ErrEndOfBuildEventStream))
		})

		It("can keep track of workers", func() {
			Expect(database.Workers()).To(BeEmpty())

			infoA := db.WorkerInfo{
				GardenAddr:       "1.2.3.4:7777",
				BaggageclaimURL:  "5.6.7.8:7788",
				ActiveContainers: 42,
				ResourceTypes: []atc.WorkerResourceType{
					{Type: "some-resource-a", Image: "some-image-a"},
				},
				Platform: "webos",
				Tags:     []string{"palm", "was", "great"},
				Name:     "workerName1",
			}

			infoB := db.WorkerInfo{
				GardenAddr:       "1.2.3.4:8888",
				ActiveContainers: 42,
				ResourceTypes: []atc.WorkerResourceType{
					{Type: "some-resource-b", Image: "some-image-b"},
				},
				Platform: "plan9",
				Tags:     []string{"russ", "cox", "was", "here"},
			}

			By("persisting workers with no TTLs")
			err := database.SaveWorker(infoA, 0)
			Expect(err).NotTo(HaveOccurred())

			Expect(database.Workers()).To(ConsistOf(infoA))

			By("being idempotent")
			err = database.SaveWorker(infoA, 0)
			Expect(err).NotTo(HaveOccurred())

			Expect(database.Workers()).To(ConsistOf(infoA))

			By("expiring TTLs")
			ttl := 1 * time.Second

			err = database.SaveWorker(infoB, ttl)
			Expect(err).NotTo(HaveOccurred())

			infoB.Name = "1.2.3.4:8888"

			Consistently(database.Workers, ttl/2).Should(ConsistOf(infoA, infoB))
			Eventually(database.Workers, 2*ttl).Should(ConsistOf(infoA))

			By("overwriting TTLs")
			err = database.SaveWorker(infoA, ttl)
			Expect(err).NotTo(HaveOccurred())

			Consistently(database.Workers, ttl/2).Should(ConsistOf(infoA))
			Eventually(database.Workers, 2*ttl).Should(BeEmpty())
		})

		It("it can keep track of a worker", func() {
			By("calling it with worker names that do not exist")

			workerInfo, found, err := database.GetWorker("nope")
			Expect(err).NotTo(HaveOccurred())
			Expect(workerInfo).To(Equal(db.WorkerInfo{}))
			Expect(found).To(BeFalse())

			infoA := db.WorkerInfo{
				GardenAddr:       "1.2.3.4:7777",
				BaggageclaimURL:  "http://5.6.7.8:7788",
				ActiveContainers: 42,
				ResourceTypes: []atc.WorkerResourceType{
					{Type: "some-resource-a", Image: "some-image-a"},
				},
				Platform: "webos",
				Tags:     []string{"palm", "was", "great"},
				Name:     "workerName1",
			}

			infoB := db.WorkerInfo{
				GardenAddr:       "1.2.3.4:8888",
				BaggageclaimURL:  "http://5.6.7.8:8899",
				ActiveContainers: 42,
				ResourceTypes: []atc.WorkerResourceType{
					{Type: "some-resource-b", Image: "some-image-b"},
				},
				Platform: "plan9",
				Tags:     []string{"russ", "cox", "was", "here"},
				Name:     "workerName2",
			}

			infoC := db.WorkerInfo{
				GardenAddr:       "1.2.3.5:8888",
				BaggageclaimURL:  "http://5.6.7.9:8899",
				ActiveContainers: 42,
				ResourceTypes: []atc.WorkerResourceType{
					{Type: "some-resource-b", Image: "some-image-b"},
				},
				Platform: "plan9",
				Tags:     []string{"russ", "cox", "was", "here"},
			}

			err = database.SaveWorker(infoA, 0)
			Expect(err).NotTo(HaveOccurred())

			err = database.SaveWorker(infoB, 0)
			Expect(err).NotTo(HaveOccurred())

			err = database.SaveWorker(infoC, 0)
			Expect(err).NotTo(HaveOccurred())

			By("returning one workerinfo by worker name")
			workerInfo, found, err = database.GetWorker("workerName2")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(workerInfo).To(Equal(infoB))

			By("returning one workerinfo by addr if name is null")
			workerInfo, found, err = database.GetWorker("1.2.3.5:8888")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(workerInfo.Name).To(Equal("1.2.3.5:8888"))

			By("expiring TTLs")
			ttl := 1 * time.Second

			err = database.SaveWorker(infoA, ttl)
			Expect(err).NotTo(HaveOccurred())

			workerFound := func() bool {
				_, found, _ = database.GetWorker("workerName1")
				return found
			}

			Consistently(workerFound, ttl/2).Should(BeTrue())
			Eventually(workerFound, 2*ttl).Should(BeFalse())
		})

		It("can create and get a container info object", func() {
			expectedContainerInfo := db.ContainerInfo{
				Handle:       "some-handle",
				Name:         "some-container",
				PipelineName: "some-pipeline",
				BuildID:      123,
				Type:         db.ContainerTypeTask,
				WorkerName:   "some-worker",
			}

			By("creating a container")
			err := database.CreateContainerInfo(expectedContainerInfo, time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("trying to create a container with the same handle")
			err = database.CreateContainerInfo(db.ContainerInfo{Handle: "some-handle"}, time.Second)
			Expect(err).To(HaveOccurred())

			By("getting the saved info object by handle")
			actualContainerInfo, found, err := database.GetContainerInfo("some-handle")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(actualContainerInfo.Handle).To(Equal("some-handle"))
			Expect(actualContainerInfo.Name).To(Equal("some-container"))
			Expect(actualContainerInfo.PipelineName).To(Equal("some-pipeline"))
			Expect(actualContainerInfo.BuildID).To(Equal(123))
			Expect(actualContainerInfo.Type).To(Equal(db.ContainerTypeTask))
			Expect(actualContainerInfo.WorkerName).To(Equal("some-worker"))

			By("returning found = false when getting by a handle that does not exist")
			_, found, err = database.GetContainerInfo("nope")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
		})

		It("can update the time to live for a container info object", func() {
			updatedTTL := 5 * time.Minute

			originalContainerInfo := db.ContainerInfo{
				Handle: "some-handle",
				Type:   db.ContainerTypeTask,
			}
			err := database.CreateContainerInfo(originalContainerInfo, time.Minute)
			Expect(err).NotTo(HaveOccurred())

			// comparisonContainerInfo is used to get the expected expiration time in the
			// database timezone to avoid timezone errors
			comparisonContainerInfo := db.ContainerInfo{
				Handle: "comparison-handle",
				Type:   db.ContainerTypeTask,
			}
			err = database.CreateContainerInfo(comparisonContainerInfo, updatedTTL)
			Expect(err).NotTo(HaveOccurred())

			comparisonContainerInfo, found, err := database.GetContainerInfo("comparison-handle")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			err = database.UpdateExpiresAtOnContainerInfo("some-handle", updatedTTL)
			Expect(err).NotTo(HaveOccurred())

			updatedContainerInfo, found, err := database.GetContainerInfo("some-handle")
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())

			Expect(updatedContainerInfo.ExpiresAt).To(BeTemporally("~", comparisonContainerInfo.ExpiresAt, time.Second))
		})

		type findContainerInfosByIdentifierExample struct {
			containersToCreate   []db.ContainerInfo
			identifierToFilerFor db.ContainerIdentifier
			expectedHandles      []string
		}

		DescribeTable("filtering containers by identifier",
			func(example findContainerInfosByIdentifierExample) {
				var results []db.ContainerInfo
				var handles []string
				var found bool
				var err error

				for _, containerToCreate := range example.containersToCreate {
					if containerToCreate.Type.ToString() == "" {
						containerToCreate.Type = db.ContainerTypeTask
					}

					err = database.CreateContainerInfo(containerToCreate, 1*time.Minute)
					Expect(err).NotTo(HaveOccurred())
				}

				results, found, err = database.FindContainerInfosByIdentifier(example.identifierToFilerFor)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(Equal(example.expectedHandles != nil))

				for _, result := range results {
					handles = append(handles, result.Handle)
				}

				Expect(handles).To(ConsistOf(example.expectedHandles))

				for _, containerToDelete := range example.containersToCreate {
					err = database.DeleteContainerInfo(containerToDelete.Handle)
					Expect(err).NotTo(HaveOccurred())
				}
			},

			Entry("returns everything when no filters are passed", findContainerInfosByIdentifierExample{
				containersToCreate: []db.ContainerInfo{
					{Handle: "a"},
					{Handle: "b"},
				},
				identifierToFilerFor: db.ContainerIdentifier{},
				expectedHandles:      []string{"a", "b"},
			}),

			Entry("does not return things that the filter doesn't match", findContainerInfosByIdentifierExample{
				containersToCreate: []db.ContainerInfo{
					{Handle: "a"},
					{Handle: "b"},
				},
				identifierToFilerFor: db.ContainerIdentifier{Name: "some-name"},
				expectedHandles:      nil,
			}),

			Entry("returns containers where the name matches", findContainerInfosByIdentifierExample{
				containersToCreate: []db.ContainerInfo{
					{Handle: "a", Name: "some-container"},
					{Handle: "b", Name: "some-container"},
					{Handle: "c", Name: "some-other"},
				},
				identifierToFilerFor: db.ContainerIdentifier{Name: "some-container"},
				expectedHandles:      []string{"a", "b"},
			}),

			Entry("returns containers where the pipeline matches", findContainerInfosByIdentifierExample{
				containersToCreate: []db.ContainerInfo{
					{Handle: "a", PipelineName: "some-pipeline"},
					{Handle: "b", PipelineName: "some-other"},
					{Handle: "c", PipelineName: "some-pipeline"},
				},
				identifierToFilerFor: db.ContainerIdentifier{PipelineName: "some-pipeline"},
				expectedHandles:      []string{"a", "c"},
			}),

			Entry("returns containers where the build id matches", findContainerInfosByIdentifierExample{
				containersToCreate: []db.ContainerInfo{
					{Handle: "a", BuildID: 1},
					{Handle: "b", BuildID: 2},
					{Handle: "c", BuildID: 2},
				},
				identifierToFilerFor: db.ContainerIdentifier{BuildID: 2},
				expectedHandles:      []string{"b", "c"},
			}),

			Entry("returns containers where the type matches", findContainerInfosByIdentifierExample{
				containersToCreate: []db.ContainerInfo{
					{Handle: "a", Type: db.ContainerTypePut},
					{Handle: "b", Type: db.ContainerTypePut},
					{Handle: "c", Type: db.ContainerTypeGet},
				},
				identifierToFilerFor: db.ContainerIdentifier{Type: db.ContainerTypePut},
				expectedHandles:      []string{"a", "b"},
			}),

			Entry("returns containers where the worker name matches", findContainerInfosByIdentifierExample{
				containersToCreate: []db.ContainerInfo{
					{Handle: "a", WorkerName: "some-worker"},
					{Handle: "b", WorkerName: "some-worker"},
					{Handle: "c", WorkerName: "other"},
				},
				identifierToFilerFor: db.ContainerIdentifier{WorkerName: "some-worker"},
				expectedHandles:      []string{"a", "b"},
			}),

			Entry("returns containers where all fields match", findContainerInfosByIdentifierExample{
				containersToCreate: []db.ContainerInfo{
					{
						Handle:       "a",
						Name:         "some-name",
						PipelineName: "some-pipeline",
						BuildID:      123,
						Type:         db.ContainerTypeCheck,
						WorkerName:   "some-worker",
					},
					{
						Handle:       "b",
						Name:         "WROONG",
						PipelineName: "some-pipeline",
						BuildID:      123,
						Type:         db.ContainerTypeCheck,
						WorkerName:   "some-worker",
					},
					{
						Handle:       "c",
						Name:         "some-name",
						PipelineName: "some-pipeline",
						BuildID:      123,
						Type:         db.ContainerTypeCheck,
						WorkerName:   "some-worker"},
					{
						Handle:     "d",
						WorkerName: "Wat",
					},
				},
				identifierToFilerFor: db.ContainerIdentifier{
					Name:         "some-name",
					PipelineName: "some-pipeline",
					BuildID:      123,
					Type:         db.ContainerTypeCheck,
					WorkerName:   "some-worker",
				},
				expectedHandles: []string{"a", "c"},
			}),
		)

		It("can find a single container info by identifier", func() {
			expectedContainerInfo := db.ContainerInfo{
				Handle: "some-handle",
				Name:   "some-container",
				Type:   db.ContainerTypeTask,
			}
			otherContainerInfo := db.ContainerInfo{
				Handle: "other-handle",
				Name:   "other-container",
				Type:   db.ContainerTypeTask,
			}

			err := database.CreateContainerInfo(expectedContainerInfo, time.Minute)
			Expect(err).NotTo(HaveOccurred())
			err = database.CreateContainerInfo(otherContainerInfo, time.Minute)
			Expect(err).NotTo(HaveOccurred())

			By("returning a single matching container info")
			actualContainerInfo, found, err := database.FindContainerInfoByIdentifier(db.ContainerIdentifier{Name: "some-container"})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(actualContainerInfo).To(Equal(expectedContainerInfo))

			By("erroring if more than one container matches the filter")
			actualContainerInfo, found, err = database.FindContainerInfoByIdentifier(db.ContainerIdentifier{Type: db.ContainerTypeTask})
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(db.ErrMultipleContainersFound))
			Expect(found).To(BeFalse())
			Expect(actualContainerInfo.Handle).To(BeEmpty())

			By("returning found of false if no containers match the filter")
			actualContainerInfo, found, err = database.FindContainerInfoByIdentifier(db.ContainerIdentifier{Name: "nope"})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeFalse())
			Expect(actualContainerInfo.Handle).To(BeEmpty())

			By("removing it if the TTL has expired")
			ttl := 1 * time.Second
			ttlContainerInfo := db.ContainerInfo{
				Handle: "some-ttl-handle",
				Name:   "some-ttl-name",
				Type:   db.ContainerTypeTask,
			}

			err = database.CreateContainerInfo(ttlContainerInfo, -ttl)
			Expect(err).NotTo(HaveOccurred())
			_, found, err = database.FindContainerInfoByIdentifier(db.ContainerIdentifier{Name: "some-ttl-name"})
			Expect(found).To(BeFalse())
		})

		It("can create one-off builds with increasing names", func() {
			oneOff, err := database.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			Expect(oneOff.ID).NotTo(BeZero())
			Expect(oneOff.JobName).To(BeZero())
			Expect(oneOff.Name).To(Equal("1"))
			Expect(oneOff.Status).To(Equal(db.StatusPending))

			oneOffGot, found, err := database.GetBuild(oneOff.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
			Expect(oneOffGot).To(Equal(oneOff))

			jobBuild, err := database.PipelineDB.CreateJobBuild("some-other-job")
			Expect(err).NotTo(HaveOccurred())
			Expect(jobBuild.Name).To(Equal("1"))

			nextOneOff, err := database.CreateOneOffBuild()
			Expect(err).NotTo(HaveOccurred())
			Expect(nextOneOff.ID).NotTo(BeZero())
			Expect(nextOneOff.ID).NotTo(Equal(oneOff.ID))
			Expect(nextOneOff.JobName).To(BeZero())
			Expect(nextOneOff.Name).To(Equal("2"))
			Expect(nextOneOff.Status).To(Equal(db.StatusPending))

			allBuilds, err := database.GetAllBuilds()
			Expect(err).NotTo(HaveOccurred())
			Expect(allBuilds).To(Equal([]db.Build{nextOneOff, jobBuild, oneOff}))
		})

		Describe("GetAllStartedBuilds", func() {
			var build1 db.Build
			var build2 db.Build
			BeforeEach(func() {
				var err error

				build1, err = database.CreateOneOffBuild()
				Expect(err).NotTo(HaveOccurred())

				build2, err = database.PipelineDB.CreateJobBuild("some-job")
				Expect(err).NotTo(HaveOccurred())

				_, err = database.CreateOneOffBuild()
				Expect(err).NotTo(HaveOccurred())

				started, err := database.StartBuild(build1.ID, "some-engine", "so-meta")
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())

				started, err = database.StartBuild(build2.ID, "some-engine", "so-meta")
				Expect(err).NotTo(HaveOccurred())
				Expect(started).To(BeTrue())
			})

			It("returns all builds that have been started, regardless of pipeline", func() {
				builds, err := database.GetAllStartedBuilds()
				Expect(err).NotTo(HaveOccurred())

				Expect(len(builds)).To(Equal(2))

				build1, found, err := database.GetBuild(build1.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())
				build2, found, err := database.GetBuild(build2.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(found).To(BeTrue())

				Expect(builds).To(ConsistOf(build1, build2))
			})
		})
	}
}

type someLock string

func (lock someLock) Name() string {
	return "some-lock:" + string(lock)
}
