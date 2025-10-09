from flask import Flask
import hashlib
import time
import logging
import sys

app = Flask(__name__)
request_count = 0

logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(message)s',
    stream=sys.stdout
)
logger = logging.getLogger(__name__)


def cpu_intensive_work():
    """Burn CPU to trigger autoscaling"""
    result = ""
    for i in range(500000):
        result = hashlib.sha256(f"dummy-work-{i}".encode()).hexdigest()
    return result

@app.route('/')
def hello():
    global request_count
    request_count += 1
    
    # Measure CPU work time
    start_time = time.time()
    hash_result = cpu_intensive_work()
    cpu_time = time.time() - start_time
  
    logger.info(f"REQUEST #{request_count} - CPU time: {cpu_time:.3f}s - Hash: {hash_result[:8]}")

    return '''
    <p>Request count: {}</p>
    <p>CPU work time: {:.3f} seconds</p>
    <p>Hash computed: {}...</p>
    '''.format(request_count, cpu_time, hash_result[:16])

if __name__ == '__main__':
    app.run(host='0.0.0.0', port=8080)
