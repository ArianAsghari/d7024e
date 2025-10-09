#!/bin/bash

# Load test script for https://hello.ltu-m7011e-johan.se
# This will generate a large number of requests

URL="https://hello.ltu-m7011e-johan.se"
CONCURRENT_REQUESTS=50
DURATION_SECONDS=300  # 5 minutes

echo "Starting load test..."
echo "Target: $URL"
echo "Concurrent requests: $CONCURRENT_REQUESTS"
echo "Duration: $DURATION_SECONDS seconds"
echo "---"

# Function to make a request
make_request() {
    curl -s -o /dev/null -w "%{http_code}\n" "$URL"
}

# Export function so subshells can use it
export -f make_request
export URL

# Start time
START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION_SECONDS))

REQUEST_COUNT=0

# Run requests in parallel
while [ $(date +%s) -lt $END_TIME ]; do
    # Launch concurrent requests in background
    for i in $(seq 1 $CONCURRENT_REQUESTS); do
        make_request &
        ((REQUEST_COUNT++))
    done
    
    # Wait for batch to complete
    wait
    
    # Small delay to prevent overwhelming the system
    sleep 0.1
    
    # Show progress every 100 requests
    if [ $((REQUEST_COUNT % 100)) -eq 0 ]; then
        ELAPSED=$(($(date +%s) - START_TIME))
        if [ $ELAPSED -gt 0 ]; then
            RATE=$((REQUEST_COUNT / ELAPSED))
            echo "Requests sent: $REQUEST_COUNT | Elapsed: ${ELAPSED}s | Rate: ${RATE} req/s"
        else
            echo "Requests sent: $REQUEST_COUNT | Elapsed: ${ELAPSED}s | Rate: calculating..."
        fi
    fi
done

# Wait for all background jobs to finish
wait

TOTAL_TIME=$(($(date +%s) - START_TIME))

echo "---"
echo "Load test complete!"
echo "Total requests: $REQUEST_COUNT"
echo "Total time: ${TOTAL_TIME}s"

# Calculate rate safely
if [ $TOTAL_TIME -gt 0 ]; then
    AVG_RATE=$((REQUEST_COUNT / TOTAL_TIME))
    echo "Average rate: ${AVG_RATE} requests/second"
else
    echo "Average rate: Test completed too quickly to calculate"
fi
