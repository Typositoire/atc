package scheduler_test

import (
	"errors"

	"code.cloudfoundry.org/lager"
	"code.cloudfoundry.org/lager/lagertest"
	"github.com/concourse/atc"
	"github.com/concourse/atc/db/algorithm"
	"github.com/concourse/atc/dbng"
	"github.com/concourse/atc/dbng/dbngfakes"
	"github.com/concourse/atc/engine"
	"github.com/concourse/atc/engine/enginefakes"
	"github.com/concourse/atc/scheduler"
	"github.com/concourse/atc/scheduler/inputmapper/inputmapperfakes"
	"github.com/concourse/atc/scheduler/maxinflight/maxinflightfakes"
	"github.com/concourse/atc/scheduler/schedulerfakes"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("I'm a BuildStarter", func() {
	var (
		fakePipeline     *dbngfakes.FakePipeline
		fakeUpdater      *maxinflightfakes.FakeUpdater
		fakeFactory      *schedulerfakes.FakeBuildFactory
		fakeEngine       *enginefakes.FakeEngine
		pendingBuilds    []dbng.Build
		fakeScanner      *schedulerfakes.FakeScanner
		fakeInputMapper  *inputmapperfakes.FakeInputMapper
		fakeBuildStarter *schedulerfakes.FakeBuildStarter
		fakeJob          *dbngfakes.FakeJob

		buildStarter scheduler.BuildStarter

		disaster error
	)

	BeforeEach(func() {
		fakePipeline = new(dbngfakes.FakePipeline)
		fakeUpdater = new(maxinflightfakes.FakeUpdater)
		fakeFactory = new(schedulerfakes.FakeBuildFactory)
		fakeEngine = new(enginefakes.FakeEngine)
		fakeScanner = new(schedulerfakes.FakeScanner)
		fakeInputMapper = new(inputmapperfakes.FakeInputMapper)
		fakeBuildStarter = new(schedulerfakes.FakeBuildStarter)
		fakeJob = new(dbngfakes.FakeJob)

		buildStarter = scheduler.NewBuildStarter(fakePipeline, fakeUpdater, fakeFactory, fakeScanner, fakeInputMapper, fakeEngine)

		disaster = errors.New("bad thing")
	})

	Describe("TryStartPendingBuildsForJob", func() {
		var tryStartErr error
		var createdBuild *dbngfakes.FakeBuild
		var jobConfig atc.JobConfig
		var versionedResourceTypes atc.VersionedResourceTypes

		BeforeEach(func() {
			versionedResourceTypes = atc.VersionedResourceTypes{
				{
					ResourceType: atc.ResourceType{Name: "some-resource-type"},
					Version:      atc.Version{"some": "version"},
				},
			}

			createdBuild = new(dbngfakes.FakeBuild)
			createdBuild.IDReturns(66)
			createdBuild.IsManuallyTriggeredReturns(true)

			pendingBuilds = []dbng.Build{createdBuild}
		})

		Context("when manually triggered", func() {
			BeforeEach(func() {
				jobConfig = atc.JobConfig{Name: "some-job", Plan: atc.PlanSequence{{Get: "input-1"}, {Get: "input-2"}}}
			})

			JustBeforeEach(func() {
				tryStartErr = buildStarter.TryStartPendingBuildsForJob(
					lagertest.NewTestLogger("test"),
					jobConfig,
					atc.ResourceConfigs{{Name: "some-resource"}},
					versionedResourceTypes,
					pendingBuilds,
				)
			})

			It("updates max in flight for the job", func() {
				Expect(fakeUpdater.UpdateMaxInFlightReachedCallCount()).To(Equal(1))
				_, actualJobConfig, actualBuildID := fakeUpdater.UpdateMaxInFlightReachedArgsForCall(0)
				Expect(actualJobConfig).To(Equal(jobConfig))
				Expect(actualBuildID).To(Equal(66))
			})

			Context("when max in flight is reached", func() {
				BeforeEach(func() {
					fakeUpdater.UpdateMaxInFlightReachedReturns(true, nil)
				})

				It("does not run resource check", func() {
					Expect(fakeScanner.ScanCallCount()).To(Equal(0))
				})
			})

			Context("when max in flight is not reached", func() {
				BeforeEach(func() {
					fakeUpdater.UpdateMaxInFlightReachedReturns(false, nil)
				})

				It("runs resource check for every job resource", func() {
					Expect(fakeScanner.ScanCallCount()).To(Equal(2))
				})

				Context("when resource checking fails", func() {
					BeforeEach(func() {
						fakeScanner.ScanReturns(disaster)
					})

					It("returns an error", func() {
						Expect(tryStartErr).To(Equal(disaster))
					})
				})

				Context("when resource checking succeeds", func() {
					BeforeEach(func() {
						fakeScanner.ScanStub = func(lager.Logger, string) error {
							defer GinkgoRecover()
							Expect(fakePipeline.LoadVersionsDBCallCount()).To(BeZero())
							return nil
						}
					})

					Context("when loading the versions DB fails", func() {
						BeforeEach(func() {
							fakePipeline.LoadVersionsDBReturns(nil, disaster)
						})

						It("returns an error", func() {
							Expect(tryStartErr).To(Equal(disaster))
						})

						It("checked for the right resources", func() {
							Expect(fakeScanner.ScanCallCount()).To(Equal(2))
							_, resource1 := fakeScanner.ScanArgsForCall(0)
							_, resource2 := fakeScanner.ScanArgsForCall(1)
							Expect([]string{resource1, resource2}).To(ConsistOf("input-1", "input-2"))
						})

						It("loaded the versions DB after checking all the resources", func() {
							Expect(fakePipeline.LoadVersionsDBCallCount()).To(Equal(1))
						})
					})

					Context("when loading the versions DB succeeds", func() {
						var versionsDB *algorithm.VersionsDB

						BeforeEach(func() {
							fakePipeline.LoadVersionsDBReturns(&algorithm.VersionsDB{
								ResourceVersions: []algorithm.ResourceVersion{
									{
										VersionID:  73,
										ResourceID: 127,
										CheckOrder: 123,
									},
								},
								BuildOutputs: []algorithm.BuildOutput{
									{
										ResourceVersion: algorithm.ResourceVersion{
											VersionID:  73,
											ResourceID: 127,
											CheckOrder: 123,
										},
										BuildID: 66,
										JobID:   13,
									},
								},
								BuildInputs: []algorithm.BuildInput{
									{
										ResourceVersion: algorithm.ResourceVersion{
											VersionID:  66,
											ResourceID: 77,
											CheckOrder: 88,
										},
										BuildID:   66,
										JobID:     13,
										InputName: "some-input-name",
									},
								},
								JobIDs: map[string]int{
									"bad-luck-job": 13,
								},
								ResourceIDs: map[string]int{
									"resource-127": 127,
								},
							}, nil)

							versionsDB = &algorithm.VersionsDB{JobIDs: map[string]int{"j1": 1}}
							fakePipeline.LoadVersionsDBReturns(versionsDB, nil)
						})

						Context("when saving the next input mapping fails", func() {
							BeforeEach(func() {
								fakeInputMapper.SaveNextInputMappingReturns(nil, disaster)
							})

							It("saved the next input mapping for the right job and versions", func() {
								Expect(fakeInputMapper.SaveNextInputMappingCallCount()).To(Equal(1))
								_, actualVersionsDB, actualJobConfig := fakeInputMapper.SaveNextInputMappingArgsForCall(0)
								Expect(actualVersionsDB).To(Equal(versionsDB))
								Expect(actualJobConfig).To(Equal(jobConfig))
							})
						})

						Context("when saving the next input mapping succeeds", func() {
							BeforeEach(func() {
								fakeInputMapper.SaveNextInputMappingStub = func(lager.Logger, *algorithm.VersionsDB, atc.JobConfig) (algorithm.InputMapping, error) {
									defer GinkgoRecover()
									return nil, nil
								}
							})

							It("saved the next input mapping and returns the build", func() {
								Expect(tryStartErr).NotTo(HaveOccurred())
							})
						})
					})
				})
			})
		})

		Context("when not manually triggered", func() {
			JustBeforeEach(func() {
				tryStartErr = buildStarter.TryStartPendingBuildsForJob(
					lagertest.NewTestLogger("test"),
					atc.JobConfig{Name: "some-job"},
					atc.ResourceConfigs{{Name: "some-resource"}},
					atc.VersionedResourceTypes{
						{
							ResourceType: atc.ResourceType{Name: "some-resource-type"},
							Version:      atc.Version{"some": "version"},
						},
					},
					pendingBuilds,
				)
			})

			itReturnsTheError := func() {
				It("returns the error", func() {
					Expect(tryStartErr).To(Equal(disaster))
				})
			}

			itDoesntReturnAnErrorOrMarkTheBuildAsScheduled := func() {
				It("doesn't return an error", func() {
					Expect(tryStartErr).NotTo(HaveOccurred())
				})

				It("doesn't try to mark the build as scheduled", func() {
					Expect(createdBuild.ScheduleCallCount()).To(BeZero())
				})
			}

			itUpdatedMaxInFlightForAllBuilds := func() {
				It("updated max in flight for the right jobs", func() {
					Expect(fakeUpdater.UpdateMaxInFlightReachedCallCount()).To(Equal(3))
					_, actualJobConfig, actualBuildID := fakeUpdater.UpdateMaxInFlightReachedArgsForCall(0)
					Expect(actualJobConfig).To(Equal(atc.JobConfig{Name: "some-job"}))
					Expect(actualBuildID).To(Equal(99))

					_, actualJobConfig, actualBuildID = fakeUpdater.UpdateMaxInFlightReachedArgsForCall(1)
					Expect(actualJobConfig).To(Equal(atc.JobConfig{Name: "some-job"}))
					Expect(actualBuildID).To(Equal(999))
				})
			}

			itUpdatedMaxInFlightForTheFirstBuild := func() {
				It("updated max in flight for the first jobs", func() {
					Expect(fakeUpdater.UpdateMaxInFlightReachedCallCount()).To(Equal(1))
					_, actualJobConfig, actualBuildID := fakeUpdater.UpdateMaxInFlightReachedArgsForCall(0)
					Expect(actualJobConfig).To(Equal(atc.JobConfig{Name: "some-job"}))
					Expect(actualBuildID).To(Equal(99))
				})
			}

			Context("when the stars align", func() {
				BeforeEach(func() {
					fakeJob.PausedReturns(false)
					fakeUpdater.UpdateMaxInFlightReachedReturns(false, nil)
					fakePipeline.GetNextBuildInputsReturns([]dbng.BuildInput{{Name: "some-input"}}, true, nil)
					fakePipeline.PausedReturns(false)
					fakePipeline.JobReturns(fakeJob, true, nil)
				})

				Context("when there are several pending builds", func() {
					var pendingBuild1 *dbngfakes.FakeBuild
					var pendingBuild2 *dbngfakes.FakeBuild
					var pendingBuild3 *dbngfakes.FakeBuild

					BeforeEach(func() {
						pendingBuild1 = new(dbngfakes.FakeBuild)
						pendingBuild1.IDReturns(99)
						pendingBuild1.ScheduleReturns(true, nil)
						pendingBuild2 = new(dbngfakes.FakeBuild)
						pendingBuild2.IDReturns(999)
						pendingBuild2.ScheduleReturns(true, nil)
						pendingBuild3 = new(dbngfakes.FakeBuild)
						pendingBuild3.IDReturns(555)
						pendingBuild3.ScheduleReturns(true, nil)
						pendingBuilds = []dbng.Build{pendingBuild1, pendingBuild2, pendingBuild3}
					})

					Context("when marking the build as scheduled fails", func() {
						BeforeEach(func() {
							pendingBuild1.ScheduleReturns(false, disaster)
						})

						It("returns the error", func() {
							Expect(tryStartErr).To(Equal(disaster))
						})

						It("marked the right build as scheduled", func() {
							Expect(pendingBuild1.ScheduleCallCount()).To(Equal(1))
						})
					})

					Context("when someone else already scheduled the build", func() {
						BeforeEach(func() {
							pendingBuild1.ScheduleReturns(false, nil)
						})

						It("doesn't return an error", func() {
							Expect(tryStartErr).NotTo(HaveOccurred())
						})

						It("doesn't try to use inputs for build", func() {
							Expect(pendingBuild1.UseInputsCallCount()).To(BeZero())
						})
					})

					Context("when marking the build as scheduled succeeds", func() {
						BeforeEach(func() {
							pendingBuild1.ScheduleReturns(true, nil)
						})

						Context("when using inputs for build fails", func() {
							BeforeEach(func() {
								pendingBuild1.UseInputsReturns(disaster)
							})

							It("returns the error", func() {
								Expect(tryStartErr).To(Equal(disaster))
							})

							It("used the right inputs for the right build", func() {
								Expect(pendingBuild1.UseInputsCallCount()).To(Equal(1))
								actualInputs := pendingBuild1.UseInputsArgsForCall(0)
								Expect(actualInputs).To(Equal([]dbng.BuildInput{{Name: "some-input"}}))
							})
						})

						Context("when using inputs for build succeeds", func() {
							BeforeEach(func() {
								pendingBuild1.UseInputsReturns(nil)
							})

							Context("when creating the build plan fails", func() {
								BeforeEach(func() {
									fakeFactory.CreateReturns(atc.Plan{}, disaster)
								})

								It("stops creating builds for job", func() {
									Expect(fakeFactory.CreateCallCount()).To(Equal(1))
									actualJobConfig, actualResourceConfigs, actualResourceTypes, actualBuildInputs := fakeFactory.CreateArgsForCall(0)
									Expect(actualJobConfig).To(Equal(atc.JobConfig{Name: "some-job"}))
									Expect(actualResourceConfigs).To(Equal(atc.ResourceConfigs{{Name: "some-resource"}}))
									Expect(actualResourceTypes).To(Equal(versionedResourceTypes))
									Expect(actualBuildInputs).To(Equal([]dbng.BuildInput{{Name: "some-input"}}))
								})

								Context("when marking the build as errored fails", func() {
									BeforeEach(func() {
										pendingBuild1.FinishReturns(disaster)
									})

									It("doesn't return an error", func() {
										Expect(tryStartErr).NotTo(HaveOccurred())
									})

									It("marked the right build as errored", func() {
										Expect(pendingBuild1.FinishCallCount()).To(Equal(1))
										actualStatus := pendingBuild1.FinishArgsForCall(0)
										Expect(actualStatus).To(Equal(dbng.BuildStatusErrored))
									})
								})

								Context("when marking the build as errored succeeds", func() {
									BeforeEach(func() {
										pendingBuild1.FinishReturns(nil)
									})

									It("doesn't return an error", func() {
										Expect(tryStartErr).NotTo(HaveOccurred())
									})
								})
							})

							Context("when creating the build plan succeeds", func() {
								BeforeEach(func() {
									fakeFactory.CreateReturns(atc.Plan{Task: &atc.TaskPlan{ConfigPath: "some-task-1.yml"}}, nil)
									fakeEngine.CreateBuildReturns(new(enginefakes.FakeBuild), nil)
								})

								It("creates build plans for all builds", func() {
									Expect(fakeFactory.CreateCallCount()).To(Equal(3))
									actualJobConfig, actualResourceConfigs, actualResourceTypes, actualBuildInputs := fakeFactory.CreateArgsForCall(0)
									Expect(actualJobConfig).To(Equal(atc.JobConfig{Name: "some-job"}))
									Expect(actualResourceConfigs).To(Equal(atc.ResourceConfigs{{Name: "some-resource"}}))
									Expect(actualResourceTypes).To(Equal(versionedResourceTypes))
									Expect(actualBuildInputs).To(Equal([]dbng.BuildInput{{Name: "some-input"}}))

									actualJobConfig, actualResourceConfigs, actualResourceTypes, actualBuildInputs = fakeFactory.CreateArgsForCall(1)
									Expect(actualJobConfig).To(Equal(atc.JobConfig{Name: "some-job"}))
									Expect(actualResourceConfigs).To(Equal(atc.ResourceConfigs{{Name: "some-resource"}}))
									Expect(actualResourceTypes).To(Equal(versionedResourceTypes))
									Expect(actualBuildInputs).To(Equal([]dbng.BuildInput{{Name: "some-input"}}))

									actualJobConfig, actualResourceConfigs, actualResourceTypes, actualBuildInputs = fakeFactory.CreateArgsForCall(2)
									Expect(actualJobConfig).To(Equal(atc.JobConfig{Name: "some-job"}))
									Expect(actualResourceConfigs).To(Equal(atc.ResourceConfigs{{Name: "some-resource"}}))
									Expect(actualResourceTypes).To(Equal(versionedResourceTypes))
									Expect(actualBuildInputs).To(Equal([]dbng.BuildInput{{Name: "some-input"}}))
								})

								Context("when creating the engine build fails", func() {
									BeforeEach(func() {
										fakeEngine.CreateBuildReturns(nil, disaster)
									})

									It("doesn't return an error", func() {
										Expect(tryStartErr).NotTo(HaveOccurred())
									})
								})

								Context("when creating the engine build succeeds", func() {
									var engineBuild1 *enginefakes.FakeBuild
									var engineBuild2 *enginefakes.FakeBuild
									var engineBuild3 *enginefakes.FakeBuild

									BeforeEach(func() {
										engineBuild1 = new(enginefakes.FakeBuild)
										engineBuild2 = new(enginefakes.FakeBuild)
										engineBuild3 = new(enginefakes.FakeBuild)
										createBuildCallCount := 0
										fakeEngine.CreateBuildStub = func(lager.Logger, dbng.Build, atc.Plan) (engine.Build, error) {
											createBuildCallCount++
											switch createBuildCallCount {
											case 1:
												return engineBuild1, nil
											case 2:
												return engineBuild2, nil
											case 3:
												return engineBuild3, nil
											default:
												panic("unexpected-call-count-for-create-build")
											}
										}
									})

									It("doesn't return an error", func() {
										Expect(tryStartErr).NotTo(HaveOccurred())
									})

									itUpdatedMaxInFlightForAllBuilds()

									It("created the engine build with the right build and plan", func() {
										Expect(fakeEngine.CreateBuildCallCount()).To(Equal(3))
										_, actualBuild, actualPlan := fakeEngine.CreateBuildArgsForCall(0)
										Expect(actualBuild).To(Equal(pendingBuild1))
										Expect(actualPlan).To(Equal(atc.Plan{Task: &atc.TaskPlan{ConfigPath: "some-task-1.yml"}}))

										_, actualBuild, actualPlan = fakeEngine.CreateBuildArgsForCall(1)
										Expect(actualBuild).To(Equal(pendingBuild2))
										Expect(actualPlan).To(Equal(atc.Plan{Task: &atc.TaskPlan{ConfigPath: "some-task-1.yml"}}))

										_, actualBuild, actualPlan = fakeEngine.CreateBuildArgsForCall(2)
										Expect(actualBuild).To(Equal(pendingBuild3))
										Expect(actualPlan).To(Equal(atc.Plan{Task: &atc.TaskPlan{ConfigPath: "some-task-1.yml"}}))
									})

									It("starts the engine build (asynchronously)", func() {
										Eventually(engineBuild1.ResumeCallCount).Should(Equal(1))
										Eventually(engineBuild2.ResumeCallCount).Should(Equal(1))
										Eventually(engineBuild3.ResumeCallCount).Should(Equal(1))
									})
								})
							})
						})
					})

					Context("when updating max in flight reached fails", func() {
						BeforeEach(func() {
							fakeUpdater.UpdateMaxInFlightReachedReturns(false, disaster)
						})

						itReturnsTheError()
						itUpdatedMaxInFlightForTheFirstBuild()
					})

					Context("when max in flight is reached", func() {
						BeforeEach(func() {
							fakeUpdater.UpdateMaxInFlightReachedReturns(true, nil)
						})

						itDoesntReturnAnErrorOrMarkTheBuildAsScheduled()
					})

					Context("when getting the next build inputs fails", func() {
						BeforeEach(func() {
							fakePipeline.GetNextBuildInputsReturns(nil, false, disaster)
						})

						itReturnsTheError()
						itUpdatedMaxInFlightForTheFirstBuild()
					})

					Context("when there are no next build inputs", func() {
						BeforeEach(func() {
							fakePipeline.GetNextBuildInputsReturns(nil, false, nil)
						})

						itDoesntReturnAnErrorOrMarkTheBuildAsScheduled()
						itUpdatedMaxInFlightForTheFirstBuild()
					})

					Context("when checking if the pipeline is paused fails", func() {
						BeforeEach(func() {
							fakePipeline.CheckPausedReturns(false, disaster)
						})

						itReturnsTheError()
						itUpdatedMaxInFlightForTheFirstBuild()
					})

					Context("when the pipeline is paused", func() {
						BeforeEach(func() {
							fakePipeline.CheckPausedReturns(true, nil)
						})

						itDoesntReturnAnErrorOrMarkTheBuildAsScheduled()
						itUpdatedMaxInFlightForTheFirstBuild()
					})

					Context("when getting the job fails", func() {
						BeforeEach(func() {
							fakePipeline.JobReturns(nil, false, disaster)
						})

						itReturnsTheError()
						itUpdatedMaxInFlightForTheFirstBuild()
					})

					Context("when the job is paused", func() {
						BeforeEach(func() {
							fakeJob.PausedReturns(true)
							fakePipeline.JobReturns(fakeJob, true, nil)
						})

						itDoesntReturnAnErrorOrMarkTheBuildAsScheduled()
						itUpdatedMaxInFlightForTheFirstBuild()
					})
				})
			})
		})
	})

})
