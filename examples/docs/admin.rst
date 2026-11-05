Administration
==============

Hosts
-----

=====  ========
Host   SSH port
=====  ========
web1   2222
web2   2222
=====  ========

Ports
-----

=======  ====
Service  Port
=======  ====
api      8080
admin    9090
=======  ====

Start
-----

.. code-block:: bash

   docker run -p 8080:8080 larder:1.4.0

Restore
-------

.. code-block:: bash

   larder restore --from /var/backups/larder
