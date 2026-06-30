'use strict';

// Fetch actions from database via API (synchronous to ensure availability before page scripts run)
(function() {
    var fallback = [
        'WAIT FOR SERVICE',
        'WAIT',
        'STOP AT LOCATION',
        'LOAD PASSENGERS',
        'UNLOAD PASSENGERS',
        'GO VIA LOCATION',
        'UNCOUPLE VEHICLES',
        'COUPLE TO FORMATION'
    ];

    try {
        var xhr = new XMLHttpRequest();
        xhr.open('GET', '/api/actions', false);
        xhr.send();
        if (xhr.status === 200) {
            var actions = JSON.parse(xhr.responseText);
            window.TIMETABLE_ACTIONS = actions.map(function(a) { return a.name; });
            window.TIMETABLE_ACTIONS_MAP = {};
            actions.forEach(function(a) {
                window.TIMETABLE_ACTIONS_MAP[a.name] = a.id;
            });
        } else {
            window.TIMETABLE_ACTIONS = fallback;
            window.TIMETABLE_ACTIONS_MAP = {};
        }
    } catch (e) {
        window.TIMETABLE_ACTIONS = fallback;
        window.TIMETABLE_ACTIONS_MAP = {};
    }
})();
