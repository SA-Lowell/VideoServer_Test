```javascript
// Utility function to handle AJAX errors
function handleAjaxError(xhr) {
    let errorMsg = 'An error occurred';
    if (xhr.responseJSON && xhr.responseJSON.error) {
        errorMsg = xhr.responseJSON.error;
    }
    alert('Error: ' + errorMsg);
}

// Function to debounce search inputs
function debounce(func, wait) {
    let timeout;
    return function executedFunction(...args) {
        const later = () => {
            clearTimeout(timeout);
            func(...args);
        };
        clearTimeout(timeout);
        timeout = setTimeout(later, wait);
    };
}
```