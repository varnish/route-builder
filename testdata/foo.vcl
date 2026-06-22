/* routing
name: foo_service
hostnames:
  - foo.com
  - www.foo.com
*/
vcl 4.1;
backend default { .host = "1.2.3.4"; }
