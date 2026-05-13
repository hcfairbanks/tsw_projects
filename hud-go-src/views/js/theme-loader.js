/**
 * TSW HUD Project - Theme Loader
 * Include this script to automatically apply the user's theme preference
 * and color scheme for accessibility
 */
(function() {
    // Apply theme from localStorage immediately (before DOM loads)
    function applyTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
        document.body && document.body.setAttribute('data-theme', theme);
    }

    // Apply color scheme for accessibility
    function applyColorScheme(scheme) {
        if (scheme && scheme !== 'default') {
            document.documentElement.setAttribute('data-color-scheme', scheme);
            document.body && document.body.setAttribute('data-color-scheme', scheme);
        } else {
            document.documentElement.removeAttribute('data-color-scheme');
            document.body && document.body.removeAttribute('data-color-scheme');
        }
    }

    // Try localStorage first for instant apply
    var savedTheme = localStorage.getItem('theme');
    if (savedTheme) {
        applyTheme(savedTheme);
    }

    var savedColorScheme = localStorage.getItem('colorScheme');
    if (savedColorScheme) {
        applyColorScheme(savedColorScheme);
    }

    // Fetch from server to ensure sync
    fetch('/api/config')
        .then(function(res) { return res.json(); })
        .then(function(config) {
            var theme = config.theme || 'dark';
            applyTheme(theme);
            localStorage.setItem('theme', theme);

            var colorScheme = config.colorScheme || 'default';
            applyColorScheme(colorScheme);
            localStorage.setItem('colorScheme', colorScheme);
        })
        .catch(function(err) {
            console.error('Failed to load config:', err);
        });

    // Re-apply when DOM is ready (in case body wasn't available initially)
    document.addEventListener('DOMContentLoaded', function() {
        var theme = localStorage.getItem('theme') || 'dark';
        applyTheme(theme);

        var colorScheme = localStorage.getItem('colorScheme') || 'default';
        applyColorScheme(colorScheme);
    });
})();
