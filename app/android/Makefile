.PHONY: check lint test build clean

# Android app quality checks: lint + test (future)
check: lint test
	@echo "All checks passed"

lint:
	./gradlew lint

test:
	@echo "No Android tests yet (placeholder for future test suite)"

build:
	./gradlew assembleDebug

clean:
	./gradlew clean
