# Track C: UX & Onboarding Progress

## C.1: Tutorial Command ✅
- Created `internal/db/migrations/002_tutorial_progress.sql` -- migration for tutorial_progress table
- Added `TutorialProgress` model to `internal/db/models.go`
- Added query methods to `internal/db/queries.go`:
  - `GetTutorialProgress(ctx, userID)` -- reads completed lessons
  - `CompleteTutorialLesson(ctx, userID, lessonNumber)` -- marks lesson complete (idempotent)
  - `ResetTutorialProgress(ctx, userID)` -- clears all progress
- Created `internal/ssh/tutorial.go` with:
  - 10 progressive lessons covering VM lifecycle, Linux basics, web servers, sharing, cleanup
  - `tutorial` command resumes from last incomplete lesson
  - `tutorial --lesson=N` jumps to specific lesson
  - `tutorial --reset` clears progress
  - Interactive command validation for hands-on lessons
  - Informational lessons for concepts (press ENTER to continue)
  - Auto-advance between lessons
- Registered `tutorial` command via init() to avoid initialization cycle
- Added `RegisterCommand()` helper for runtime command registration
- Tests in `internal/ssh/tutorial_test.go`:
  - TestValidateTutorialCommand (11 cases)
  - TestTutorialLessonsValid
  - TestTutorialProgressDB
  - TestTutorialProgressIsolation
  - TestHasArgFlag

### Notes
- Fixed stale module imports (exedevussy -> ussycode) in several files that Track A missed
- Removed dangling `cmdLLMKey` reference from commands map (function not yet implemented)
- All tests pass, build succeeds

## C.2: Browser Command
- [ ] Pending

## C.3: Doc Command
- [ ] Pending

## C.4: Project Templates
- [ ] Pending

## C.5: Welcome & MOTD
- [ ] Pending
