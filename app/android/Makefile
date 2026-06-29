.PHONY: check lint test build clean

# Android app quality checks: lint + test (future)
check: lint test
	@echo "All checks passed"

lint:
	./gradlew lint

test:
	./gradlew testDebugUnitTest

build:
	./gradlew assembleDebug

clean:
	./gradlew clean
