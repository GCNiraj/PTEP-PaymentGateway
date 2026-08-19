/* charts.js — Chart.js initialisation helpers */

let _statusChartInstance = null;
let _volumeChartInstance = null;

/**
 * Renders (or re-renders) the transaction status doughnut chart.
 * Always destroys the previous instance to prevent memory leaks.
 */
function renderStatusChart(success, failed, pending) {
  const ctx = document.getElementById('chart-doughnut');
  if (!ctx) return;

  if (_statusChartInstance) {
    _statusChartInstance.destroy();
    _statusChartInstance = null;
  }

  _statusChartInstance = new Chart(ctx, {
    type: 'doughnut',
    data: {
      labels: ['Success', 'Failed', 'Pending'],
      datasets: [{
        data: [success, failed, pending],
        backgroundColor: ['#22C55E', '#EF4444', '#F59E0B'],
        borderWidth: 0,
        hoverOffset: 4
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      cutout: '70%',
      plugins: {
        legend: {
          position: 'bottom',
          labels: { boxWidth: 12, padding: 16, font: { size: 12 } }
        }
      }
    }
  });
}

/**
 * Renders (or re-renders) the volume trend line chart.
 * @param {string[]} labels  - Date labels for X axis
 * @param {number[]} values  - Volume values for Y axis
 */
function renderVolumeChart(labels, values) {
  const ctx = document.getElementById('chart-volume');
  if (!ctx) return;

  if (_volumeChartInstance) {
    _volumeChartInstance.destroy();
    _volumeChartInstance = null;
  }

  _volumeChartInstance = new Chart(ctx, {
    type: 'line',
    data: {
      labels,
      datasets: [{
        label: 'Volume',
        data: values,
        borderColor: '#4F46E5',
        backgroundColor: 'rgba(79,70,229,0.08)',
        tension: 0.4,
        fill: true,
        pointRadius: 3,
        pointHoverRadius: 5
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: { legend: { display: false } },
      scales: {
        y: {
          beginAtZero: true,
          grid: { color: 'rgba(0,0,0,0.04)' },
          ticks: { font: { size: 11 } }
        },
        x: {
          grid: { display: false },
          ticks: { font: { size: 11 } }
        }
      }
    }
  });
}
