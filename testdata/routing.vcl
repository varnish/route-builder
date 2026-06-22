vcl 4.1;

import tls;

backend default none;

sub vcl_recv {
    if (tls.is_tls()) {
        set req.http.host = tls.authority();
    } else if (req.http.host ~ "^\[") {
        set req.http.host = regsub(req.http.host, "^\[([^\]]+)\](:\d+)?$", "[\1]");
    } else {
        set req.http.host = regsub(req.http.host, ":\d+$", "");
    }
    if (req.http.host == "foo.com" || req.http.host == "www.foo.com") {
        return(vcl(rb-label-foo_service-2026-06-22T11-13-12_530331864));
    }
    if (req.http.host == "bar.com") {
        return(vcl(rb-label-bar_service-2026-06-22T11-13-12_530331864));
    }
    return(synth(404, "No route matched"));
}
