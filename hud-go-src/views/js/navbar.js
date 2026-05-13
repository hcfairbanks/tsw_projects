/**
 * TSW HUD Project - Navbar Component
 * Include this script to automatically inject a consistent navigation bar
 *
 * Usage: Add <nav id="main-nav"></nav> where you want the navbar, then include this script
 */
(function() {
    var navLinks = [
        { href: '/start', label: 'Start', i18n: 'nav.start' },
        { href: '/timetables', label: 'Timetables', i18n: 'nav.timetables' },
        { href: '/routes', label: 'Routes', i18n: 'nav.routes' },
        { href: '/weather', label: 'Weather', i18n: 'nav.weather' },
        { href: '/map', label: 'Tracking Map', i18n: 'nav.map' },
        { href: '/settings', label: 'Settings', i18n: 'nav.settings' },
        { href: '/countries', label: 'Countries', i18n: 'nav.countries', devOnly: true },
        { href: '/locations', label: 'Locations', i18n: 'nav.locations', devOnly: true },
        { href: '/formations', label: 'Formations', i18n: 'nav.formations', devOnly: true },
        { href: '/train-classes', label: 'Train Classes', i18n: 'nav.trainClasses' },
        { href: '/extractor', label: 'Extractor', i18n: 'nav.extractor' }
    ];

    // Language data for rendering flags (must match i18n.js SUPPORTED_LANGUAGES)
    var languages = [
        { code: 'en', flag: 'gb', name: 'English' },
        { code: 'en-US', flag: 'us', name: 'English (US)' },
        { code: 'fr', flag: 'fr', name: 'Français' },
        { code: 'de', flag: 'de', name: 'Deutsch' },
        { code: 'it', flag: 'it', name: 'Italiano' },
        { code: 'es', flag: 'es', name: 'Español' },
        { code: 'pl', flag: 'pl', name: 'Polski' },
        { code: 'ru', flag: 'ru', name: 'Русский' },
        { code: 'zh', flag: 'cn', name: '中文' },
        { code: 'ja', flag: 'jp', name: '日本語' }
    ];

    function getCurrentLanguage() {
        return localStorage.getItem('language') || 'en';
    }

    function getFlagForLang(langCode) {
        for (var i = 0; i < languages.length; i++) {
            if (languages[i].code === langCode) return languages[i].flag;
        }
        return 'gb';
    }

    function createLanguageWidget(container) {
        var currentLang = getCurrentLanguage();
        var currentFlag = getFlagForLang(currentLang);

        // Current flag button
        var flagBtn = document.createElement('div');
        flagBtn.className = 'current-flag';
        flagBtn.innerHTML = '<span class="fi fi-' + currentFlag + '"></span><span class="dropdown-arrow">▼</span>';

        // Dropdown
        var dropdown = document.createElement('div');
        dropdown.className = 'flag-dropdown';

        languages.forEach(function(lang) {
            var option = document.createElement('div');
            option.className = 'flag-option' + (lang.code === currentLang ? ' active' : '');
            option.dataset.lang = lang.code;
            option.title = lang.name;
            option.innerHTML = '<span class="fi fi-' + lang.flag + '"></span>';
            option.addEventListener('click', function(e) {
                e.stopPropagation();
                if (typeof i18n !== 'undefined' && i18n.setLanguage) {
                    i18n.setLanguage(lang.code);
                }
                dropdown.classList.remove('open');
                flagBtn.classList.remove('open');
            });
            dropdown.appendChild(option);
        });

        flagBtn.addEventListener('click', function(e) {
            e.stopPropagation();
            var isOpen = dropdown.classList.toggle('open');
            flagBtn.classList.toggle('open', isOpen);
        });

        container.appendChild(flagBtn);
        container.appendChild(dropdown);

        // Close on outside click
        document.addEventListener('click', function(e) {
            if (!container.contains(e.target)) {
                dropdown.classList.remove('open');
                flagBtn.classList.remove('open');
            }
        });
    }

    function createNavbar() {
        var navContainer = document.getElementById('main-nav');
        if (!navContainer) return;

        // Add the nav class for styling
        navContainer.className = 'nav';

        // Fetch config to check development mode, then render links
        fetch('/api/config')
            .then(function(res) { return res.json(); })
            .then(function(config) { return config.developmentMode || false; })
            .catch(function() { return false; })
            .then(function(devMode) {
                renderNavLinks(navContainer, devMode);
            });
    }

    function renderNavLinks(navContainer, devMode) {
        // Create links
        navLinks.forEach(function(link) {
            if (link.devOnly && !devMode) return;

            var a = document.createElement('a');
            a.href = link.href;
            a.textContent = link.label;
            if (link.i18n) {
                a.setAttribute('data-i18n', link.i18n);
            }

            // Highlight current page
            if (window.location.pathname === link.href ||
                (link.href !== '/' && window.location.pathname.startsWith(link.href))) {
                a.style.background = 'var(--accent-color)';
                a.style.color = 'var(--bg-primary)';
            }

            navContainer.appendChild(a);
        });

        // Build language widget as a tab-style link
        var langContainer = document.createElement('a');
        langContainer.className = 'language-selector-nav';
        langContainer.href = 'javascript:void(0)';
        langContainer.style.position = 'relative';
        langContainer.style.padding = '0';
        createLanguageWidget(langContainer);
        navContainer.appendChild(langContainer);

        // Apply i18n if already loaded
        if (typeof i18n !== 'undefined' && i18n.applyTranslations) {
            i18n.applyTranslations();
        }
    }

    // Create navbar when DOM is ready
    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', createNavbar);
    } else {
        createNavbar();
    }
})();
